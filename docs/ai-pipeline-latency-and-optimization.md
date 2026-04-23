# 拉数 + AI 分析耗时长：原因、业界实践与可落地方案

本文档面向**定时任务/调度路径**中「外网热榜拉取 →（可选）兴趣 LLM 过滤 → 存库/通知」的全链路，说明**为何总耗时会很长**、**业界常见优化方向**，并结合当前 **Go 后端实现**（截至文档编写时）从多个角度提出**可演进方案**。

> 与「单批体积分片」更相关的细节见同目录 [ai-filter-batching.md](ai-filter-batching.md)。本文侧重**端到端时延、架构与产品策略**。

---

## 1. 长耗时通常来自哪里

### 1.1 串行累加

在典型整点任务中，**时间近似为各项之和**（若有串行段）：

| 阶段 | 说明 |
|------|------|
| 外网多平台热榜 | 各平台 `FetchData` 串行或受 `RequestInterval` 限流，平台数多则线性增长。 |
| 兴趣 LLM 过滤 | `Filter.FilterNews` 对多批**串行**执行；批次数 ≈ 标题数 / 每批条数（受 `batch_size` 与 `max_input_chars` 双约束） |
| 批间 sleep | 若 `ai_filter.batch_interval > 0`，**每两批之间固定等待**（毫秒级），总等待 ≈ 批次数 × 间隔。 |
| 单批 HTTP 推理 | 一次 `chat/completions` 的**首 token 时延 + 生成长度**；`max_output_tokens` 大、温度高、模型大时，完成时间可显著变长。 |
| 重试与退避 | 全局 `ai.num_retries` 对可重试错误做指数退避；兴趣过滤在 `applyAIFilterHTTPDefaults` 中会把**重试压为 1 次**（与通用 Chat 不同），但若仍有失败重试，会拉长。 |
| 超大超时 | 若 `ai.timeout` 设得**极大**（如数千秒），**不会**让单次请求变快，只在失败前挂更久，尾部拖尾时更难暴露问题。 |
| 日志与 I/O | `client.go` 在 info 级别记录**完整 request body / response body**（大 JSON 时磁盘与序列化有额外开销，通常小于网络推理，但会加重负载）。 |

**结论**：真正的大头往往是 **(1) 多批 LLM 串行 (2) 批间人为 sleep (3) 单次推理 RTT+生成**。外网拉取在平台不多时可能仅次于 LLM。

### 1.2 与「数据量/配置」的定性关系

- 兴趣长文 + 多标题 → 单批 **prompt 变长** → 预填与生成时间都上升。
- 新标题多 → `PartitionCrawlByPersistedItems` 后送入 LLM 的**候选条数**多 → 批次数增加（已实现「已落库 URL 不重复过模型」可显著减少重复小时的标题量，见 `internal/storage/news_storage.go` 与 `internal/ai/focus.go`）。

---

## 2. 业界常见优化思路（抽象层）

以下不依赖具体云厂商，属于常见工程与产品模式：

1. **异步化 / 解耦**  
   拉数与写库尽快完成；LLM 过滤或深度分析进 **消息队列/任务表**，由 worker 消费。对外接口只读**已物化**结果（与本项目「GET latest 只读库」方向一致）。

2. **小模型/规则前置**  
   用 **轻量分类器、关键词/embedding 粗排** 减少进入大模型的条数；大模型只处理少而难的 subset（成本与延迟双降）。

3. **批处理并行**（有配额与反压时）  
   多批 LLM 在**不违反 RPM/TPM** 的前提下 **worker 池并行**；与当前「多批严格串行」相对。

4. **专用推理端点**  
   自托管 **vLLM/TensorRT-LLM**、云端 **provisioned throughput** 等，降低 p99 与排队。

5. **缓存**  
   同一 title/url + 同一兴趣策略版本 → **复用**上次打分；TTL 或版本号控制失效。

6. **流式 API**（若协议支持且业务接受）  
   首包更早返回、便于心跳；**总生成时间**未必缩短，但**可感知首字节时间**可优化。

7. **观测与 SLO**  
   为「拉数 / 每批 LLM / 存库」分别打点，**定位**是网络、推理还是排队。

8. **产品侧**  
   降低「全量+实时」预期：按小时/按天汇总、只通知 Top-K、可关闭非关键分析。

---

## 3. 当前后端实现与耗时相关点（代码视角）

### 3.1 兴趣过滤：`internal/ai/filter.go`

- `FilterNews`：批与批之间**for 循环串行**；`batch_interval > 0` 时**显式 `Sleep`**。  
- `applyAIFilterHTTPDefaults`：为过滤请求设置较长超时、**重试次数降为 1**，避免过滤拖死队列。  
- `filterBatch`：每批一次 `Chat` / `ChatWithMaxOutput`；`buildUserContent` 将**兴趣全文**与**整批标题**拼进 user 消息。  

### 3.2 HTTP 客户端：`internal/ai/client.go`

- `NewAIClient`：`http.Client.Timeout` 与 `ai.timeout` 一致；`chatWithMaxOutput` 内**最多** `numRetries+1` 次 doRequest。  
- 可重试错误按指数退避 + 抖动。  
- 成功/失败均可能打印**整段**请求与响应，大 payload 时日志成本高。

### 3.3 入口：`internal/ai/focus.go` + 调度

- `ApplyFocusFilter`：扁平化多平台 → `Filter.FilterNews`；与 `ai_filter` 配置绑定。  
- 调度中若启用 `FocusFilterEnforced`，先 **PartitionCrawlByPersistedItems** 再对「新 URL」子集过模型（减少重复条数，从而**可能减少批次数**）。  

### 3.4 配置：`config/config.yaml`（示例）

- `ai_filter.batch_size` / `max_input_chars` / `batch_interval` 直接控制批次数与批间睡；**batch_interval 很大时会线性拉长总时间**。  
- `ai.timeout` / `ai.num_retries` / `ai.max_tokens` 影响单请求行为与重试。  
- `ai_analysis` 等若与调度中的「深度分析」联动（当前主路径以兴趣过滤为主），额外分析会再占一轮 LLM，需在架构上**单独**评估。  

（具体项以运行环境 `config.yaml` 为准。）

---

## 4. 多维度可落地方案（可组合）

下列按**角度**分类，可多项并行；**实施前建议**在测试环境用日志打点验证瓶颈占比。

### 4.1 配置与单批成本（零代码或极少代码）

| 方向 | 做法 |
|------|------|
| 缩短兴趣模板 | 缩短 `ai_interests.txt` 中无关叙述，**直接降低**每批 prompt 长度。 |
| 调批参数 | 在「不截断 JSON」前提下，试 **略增 `batch_size`** 或**收紧 `max_input_chars`** 使批数减少；观察失败率。 |
| 批间睡 | 若已用云 API 且无限流压力，**将 `ai_filter.batch_interval` 调为 0 或调小**；Ollama 本机可保留小间隔。 |
| 输出上限 | 保证 `ai_filter.max_output_tokens` 足够避免截断；**过大**会拉长每批生成，需在「可靠解析」与「时延」间折中。 |
| 超时与重试 | `ai.timeout` 设合理上界；避免无界等待；`num_retries` 过大时，失败重试会显著拉长**尾部**。 |

### 4.2 模型与路由

| 方向 | 做法 |
|------|------|
| 过滤用小模型 | 兴趣过滤用**更小/更快**的 chat 模型；大模型只用于少场景。 |
| 多模型路由 | 高负载时降级到次优模型；或仅 Top-N 走大模型。 |
| 专用网关 | 统一限流、队列、可观测的推理网关。 |

### 4.3 架构与执行模型

| 方向 | 做法 |
|------|------|
| 后台队列 | 定时任务**只**写「原始快照」与任务 ID；**异步 worker** 做 LLM，结果表再更新；读接口读**物化结果**（与「latest 只读库」可进一步分层）。 |
| 多批并行 | 在 RPM 允许下，**多批 FilterNews 使用 errgroup/信号量** 有限并行；需全局并发与**429 退避**协同。 |
| 拆进程 | 爬虫与 LLM 分进程，避免大内存 JSON 与推理互相干扰。 |

### 4.4 相似度/缓存

| 方向 | 做法 |
|------|------|
| 结果缓存 | Key = hash(兴趣配置版本, title 或 url)；短期 TTL 内重复条目**不调用** LLM。 |
| embedding 预过滤 | 仅当与兴趣向量**相似度**超过阈值的条进入 LLM（需 embedding 服务或本地模型）。 |

### 4.5 可观测与成本

| 方向 | 做法 |
|------|------|
| 分阶段指标 | 记录 `CrawlAll` 耗时、`Partition` 耗时、**每批** `filterBatch` 耗时、批次数。 |
| 日志降载 | 生产环境**禁止**在 info 中打印**完整** body 或加长度上限/采样，避免 I/O 放大。 |
| 成本面板 | 按日统计 token 与批次数，驱动「调参」与「产品降级」。 |

### 4.6 产品策略

| 方向 | 做法 |
|------|------|
| 仅新条过模型 | 与已实现「已落库 URL 跳过」一致；可扩展为「仅前 N 条新标题」在 strict SLO 下。 |
| 分优先级 | 高优先平台/话题先过模型，其余下小时再处理。 |

---

## 5. 建议的落地顺序（供排期参考）

1. **度量化**：在 `runCrawlAnalyzeAndNotify` 与 `FilterNews` 各段打 **elapsed_ms** 与**批次数**。  
2. **快赢**：校核 `ai_filter.batch_interval`、兴趣长度、`timeout` 与**日志体积**。  
3. **中赢**：在合规前提下评估**多批有限并行**或**小模型**做过滤。  
4. **大赢**：**异步化**整条 LLM 管道 + 只读物化表；必要时引入 **embedding/缓存**。

---

## 6. 相关文件索引

| 文件 | 与耗时的关系 |
|------|----------------|
| `internal/ai/filter.go` | 分批、批间 sleep、单批 `Chat`、user 内容体量 |
| `internal/ai/client.go` | 超时、重试、doRequest、日志开销 |
| `internal/ai/focus.go` | 扁平化、调用 `FilterNews` |
| `internal/ai/merge_crawl.go` | 合并（本地 CPU，可忽略） |
| `internal/storage/news_storage.go` | `PartitionCrawlByPersistedItems` 减少需过模型的条数 |
| `internal/scheduler/scheduler.go` | 整点串联：爬网 → 过滤 → 存库等 |
| `pkg/config` + `config/config.yaml` | `ai` / `ai_filter` 调参入口 |

---

## 7. 版本说明

- 本文描述的是**按代码结构归纳**的通用结论；**实际秒级表现**以线上配置、网络与模型供应商 SLA 为准。  
- 若 `ai-filter-batching` 与本文有细节歧义，以**代码实现**与**当前 `config.yaml`** 为准。  

```text
维护建议：在修改 Filter 或 Client 的并发/重试/日志行为时，同步更新本文件「§3/§4」中对应条目的准确性。
```
