# AI 与提示词业务说明

本文档整理后端中与 **OpenAI 兼容接口** 交互的提示词、数据流与配置项。实现主要位于 `internal/ai/`，由 `pkg/config` 中的 `filter`、`ai`、`ai_analysis`、`ai_filter` 等节驱动。

---

## 1. 能力总览

| 能力 | 包内入口 | HTTP 端点 | 典型场景 | 说明 |
|------|----------|-----------|----------|------|
| **兴趣标题过滤** | `Filter`（`filter.go`） | 无（仅调度管线） | 定时任务 | 按用户「兴趣描述」对标题打分，按 `min_score` 留条 |
| **焦点过滤编排** | `ApplyFocusFilter`（`focus.go`） | 无（仅调度管线） | 定时任务爬取后 | 编排层，实例化 Filter 并应用到全平台新闻映射 |
| **深度内容分析** | `Analyzer`（`analyzer.go`） | 无（已禁用） | 预留 `buildAINewsSummary` | 多段 JSON 结果（趋势/情感/信号等） |
| **情感分析（LLM）** | `Analyzer.AnalyzeSentiment` | 无（未接线） | 预留 | LLM 情感分析，返回 sentiment/confidence/reasoning |
| **单条新闻文章摘要** | `SummarizeNewsArticle`（`article_analyze.go`） | `POST /api/v1/news/analyze` | 前端「AI 分析」按钮 | 抓取正文 → LLM 生成报纸风格中文分析 |
| **每日行业研报** | `GenerateDayIndustryReport`（`day_industry.go`） | `POST /api/v1/news/snapshots/:date/insights` | 前端日期页「AI 研报」 | 基于当日全平台标题流生成路演体例投研报告 |
| **通用 AI 对话代理** | `PostAIChat`（`handlers_chat.go`） | `POST /api/v1/ai/chat` | 前端 AI 对话 | 转发多轮消息到 LLM，key 不暴露给前端 |
| **翻译** | `Translator`（`translator.go`） | 无 | 需业务显式调用 | 单条/批量翻译，与过滤/分析独立的提示词 |
| **关键词分析（非 LLM）** | `handlers.go` 中的分析 handler | 见下方 8 节 | 前端分析中心 | 话题趋势、情感（关键词匹配）、聚类、时期对比 |

- **兴趣说明**：见上节，可由 `config/ai_interests.txt` 提供；过滤批处理里的系统/用户**模板**仍在 `filter.go` 代码中。  
- `AIFilter.PromptFile`、`AIAnalysis.PromptFile` 等仍**未在 Go 中接文件**；`Analyzer` 提示在 `analyzer.go` 内嵌。若将来外置，需接加载器并替换下文章节说明。

---

## 2. 统一客户端与 HTTP 行为

- **端点拼接**（`client.getAPIURL`）：`api_base` 去尾 `/` 后，若已以 `/chat/completions` 结尾则直用；若以 `/v1` 结尾则拼 `.../chat/completions`；否则拼 `.../v1/chat/completions`。
- **鉴权**：`Authorization: Bearer {api_key}`（本地 Ollama 等可配占位 key）。
- **重试**：`ai.num_retries` 控制失败重试次数；**仅兴趣过滤**在 `FilterNews` 内会把客户端重试**强制改为最多 1 次**；HTTP 超时时若未设置则**继承 `ai.timeout`（秒）**（见 `filter.go`）。

---

## 3. 兴趣标题过滤（核心业务）

### 3.0 Go 后端的兴趣文件（推荐）

- 默认在 **`config/config.yaml` 同目录**放置 **`ai_interests.txt`**，并在配置中写：

```yaml
filter:
  method: "ai"
  interests_file: "ai_interests.txt"   # 相对 config.yaml 所在目录
  interests: ""                         # 可选：文件读失败时的回退内联文本
```

- 启动时 `config.Init` 在 `Unmarshal` 之后若 `interests_file` 非空，会 **`ReadFile` 并覆盖 `filter.interests`**；读失败则打日志并保留 yaml 里的 `interests`。
- 与 Python 主仓根目录的 `config/ai_interests.txt` **相互独立**；若两套都维护，大段文案建议复制同步或只维护一份再在构建/发布时同步。

### 3.1 配置

| 配置项 | 含义 |
|--------|------|
| `filter.method` | 为 `"ai"` 且（加载文件或内联后）`filter.interests` 非空时才启用 |
| `filter.interests_file` | 可选；非空则从该路径读入**全文**覆盖内联 `interests`；路径**相对** `config.yaml` 所在目录 |
| `filter.interests` | 内联兴趣说明；**若成功加载 interests_file 则被覆盖**；也可单独使用（不设 interests_file） |
| `ai_filter.min_score` | 单条**保留阈值**；≤0 时逻辑里默认 `0.7` |
| `ai_filter.batch_size` | 每批**最多**标题数；≤0 时默认 `20`；与 `max_input_chars` 取更紧约束 |
| `ai_filter.max_input_chars` | 单批 `user` 正文字符（rune）上限；`0` 表示仅按 `batch_size` 切，见 `docs/ai-filter-batching.md` |
| `ai_filter.batch_interval` | 批次间间隔（毫秒） |
| `ai_filter.max_output_tokens` | 仅过滤请求的 `max_tokens`；`0` 用全局 `ai.max_tokens` |

### 3.2 数据流

1. 将各平台 `NewsItem` 转为 `ai.NewsItem`（`Title`、`Rank`、`Source=platformID`）。  
2. 按 `batch_size` 与 `max_input_chars` 分批（串行、合并结果，见 `docs/ai-filter-batching.md`）。  
3. 每批构造 **2 条消息**：  
   - **system**：`你是一个智能新闻过滤器，请根据用户的兴趣描述过滤新闻。`  
   - **user**：见下节「用户提示词结构」。  
4. 模型需返回**纯 JSON 数组**；解析为 `index, score, reason, tags`；`index` 为**该批内**从 0 起下标。  
5. 与 `NewsItem` 映射：调度里用 `platformID + "::" + Title` 与 `GetFilteredItems` 结果做交集（见 `scheduler.applyAIFocusFilter`），请保证 **标题**与列表一致。  
6. 仅 `score >= min_score` 的条目进入后续存库/邮件/展示。

### 3.3 用户提示词（结构说明）

对每一批，用户内容等价于按顺序拼接（摘要自 `Filter.filterBatch`）：

1. 固定头：`请分析以下新闻是否符合我的兴趣，并为每条新闻打分（0-1）：`  
2. `我的兴趣描述：` + `{filter.interests}`  
3. `新闻列表：` + 多行 `序号. 标题（排名:N）`（无排名则省略括弧）  
4. 要求返回格式示例（**模型须输出可解析的 JSON 数组**），元素字段：  
   - `index`：新闻索引，从 0 开始，对应本批内顺序  
   - `score`：0–1 浮点，兴趣匹配度  
   - `reason`：短理由  
   - `tags`：字符串数组，兴趣标签  
5. 反引号包裹的代码块在解析前会去掉首尾的 `` ` ``，便于截断模型多余 markdown。

### 3.4 调参与撰写 `interests` 建议

- 描述**领域+优先级**比单纯关键词更稳（与当前 `config` 中示例风格一致）。  
- `min_score` 提高 → 更严、条数少；降低 → 更宽。  
- 批越大单次 token 越多，可配合 `batch_size` 在质量与成本间折中。  
- 过滤失败时（调度/部分逻辑会）**回退为不过滤的原始数据**，避免任务整体失败无数据。

### 3.5 其他：`Filter` 中未在主流程强用的方法

- `FilterRSS`：将 RSS 项转成伪 `NewsItem` 后复用 `FilterNews`。  
- `GetInterestedTags`：独立 system/user 提示，为文本提 3–5 标签，需返回 JSON 数组，供扩展用。

---

## 4. 深度分析 `Analyzer`（`Analyze`）

### 4.1 配置

| 配置项 | 含义 |
|--------|------|
| `ai_analysis.enabled` / `mode` / `max_news_for_analysis` | 供 `buildAINewsSummary` 与 `AnalysisConfig` 使用；`MaxNews` 每平台取前 N 条标题 |
| 当前 HTTP：`GetLatestNews` 中 **不调用** `buildAINewsSummary`，`ai_analysis` 返回 `enabled: false` 与原因 `content_ai_analysis_disabled` |

若将来在接口中恢复「详细分析」，将重新走本节的提示词与解析逻辑。

### 4.2 提示词结构

- **system**：`你是一个专业的新闻分析专家，请根据以下新闻数据进行深度分析。` + 要求从多角度分析趋势、情感等（固定中文字符串）。  
- **user**（拼接顺序）：  
  1. `请分析以下新闻数据：`  
  2. 各平台下「平台 ID + 编号标题（排名）」列表（`MaxNews` 截断）  
  3. 若有 RSS，追加 `--- RSS 新闻 ---` 与各 feed 下前 10 条标题  
  4. 要求输出**一段 JSON**（见 `analyzer.Analyze` 中模板），字段含：  
     `core_trends`、`sentiment_controversy`、`signals`、`rss_insights`、`outlook_strategy`、`standalone_summaries`（内嵌 `summary` / `highlights` 等）  
- 响应去除 ```json 包裹后做 `json.Unmarshal`；**解析失败**时仍返回结构体，**仅**填充 `raw_response` 为全文。

### 4.3 同文件其它工具提示（供扩展）

| 方法 | system / user 要点 |
|------|--------------------|
| `GenerateSummary` | 摘要，限制词数，system 为「新闻摘要生成器」 |
| `AnalyzeSentiment` | 返回 `sentiment` / `confidence` / `reasoning` 的 JSON |
| `ExtractEntities` | 实体列表 JSON 数组 |
| `ClassifyTopic` | 给定 `categories` 时做分类+置信度 |

### 4.4 `client.AnalyzeNews`

- system：`你是一个新闻分析专家，请分析以下新闻内容。`  
- user：调用方传入的 `prompt` + `新闻内容：\n` + 正文。当前偏通用封装。

---

## 5. 翻译 `Translator`

- **单条** `Translate`：system「专业翻译器」；user 为「从 X 到 Y 翻译，只返回译文」+ 文本。  
- **批量** `TranslateBatch`：在 batch 内用类似约束逐段调用。  
- 与 `config.aitranslation` 的衔接以业务调用为准，本文不展开。

---

## 6. 单条新闻文章摘要 `SummarizeNewsArticle`

**文件:** `internal/ai/article_analyze.go`

### 6.1 数据流

1. HTTP `POST /api/v1/news/analyze` → handler `PostAnalyzeNewsArticle`
2. 接收 `title`, `url`, `source_name`（可选）
3. 调用 `ai.FetchArticleWithFallback(ctx, url, mobileURL)` 抓取正文：
   - 使用 Chrome 模拟请求头
   - 403/429/503 时重试
   - 失败则降级到 Jina AI Reader (`r.jina.ai`)
4. 正文截断至 12000 runes（`maxFetchTextRunes`）
5. 将 title + body 送 LLM，system prompt 定位为「报纸编辑」，要求生成：
   - 核心事实
   - 竞争格局
   - 后续关注点
6. 输出限制 ~400 字中文，`max_tokens=2048`
7. 若正文抓取失败，基于标题生成保守摘要

### 6.2 配置

| 配置项 | 含义 |
|--------|------|
| `ai.model` / `ai.api_base` / `ai.api_key` | 共用 LLM 配置 |
| `ai.timeout` | HTTP 超时（秒） |

### 6.3 提示词要点

- **system**: 定位「报纸编辑」，输出面向行业从业者的中文分析，约 400 字
- **user**: `标题：{title}\n\n正文内容：\n{body}`
- 无正文时: `标题：{title}\n\n（注：文章正文未能成功抓取，请基于标题进行保守分析）`

---

## 7. 每日行业研报 `GenerateDayIndustryReport`

**文件:** `internal/ai/day_industry.go`

### 7.1 数据流

1. HTTP `POST /api/v1/news/snapshots/:date/insights` → handler `PostSnapshotDayInsights`
2. 查询当日所有平台 hotlist_snapshots，去重后按时间排序
3. 构建标题摘要（`buildInsightDigest`），含：
   - 平台分布统计
   - 每小时标题流（格式：`HH:00 [平台] 标题`）
4. LLM `max_tokens=8192`，生成路演/会议纪要体例的中文投研报告
5. 结果缓存到 `day_industry_reports` 表
6. 读取：`GET /api/v1/news/snapshots/:date/insights` 返回缓存

### 7.2 提示词要点

- **system**: ~40 行中文 prompt，定位「资深科技产业与投资研究专家」
- 要求覆盖 6 个维度：
  1. 宏观/市场情绪
  2. 行业与板块机会（结构性机会 + 主题催化）
  3. 可落地的线索（投融资、并购、开源、新品、政策）
  4. 从业者/普通人可参与的方向
  5. 风险、噪音与重复信息
  6. 信息局限性与合规声明
- 明确要求**不编造事实**，信号不足时标注「据标题信号」「待交叉验证」
- **user**: 标题摘要正文（含平台统计 + 小时标题流）

### 7.3 配置

| 配置项 | 含义 |
|--------|------|
| `ai.model` / `ai.api_base` | 共用 LLM 配置 |

---

## 8. 焦点过滤编排 `ApplyFocusFilter`

**文件:** `internal/ai/focus.go`

### 8.1 数据流

1. 调度器爬取完成后调用 `ApplyFocusFilter(platforms, rss)`
2. `FocusFilterEnforced()` 守卫检查：`filter.method == "ai"` 且 `filter.interests` 非空
3. 实例化 `Filter`，展平所有平台新闻为 `[]NewsItem`
4. 调用 `Filter.FilterNews()` 打分
5. 仅保留 `score >= min_score` 的条目，重建 `map[platformID][]NewsItem`
6. 过滤失败 → 回退原始数据（避免任务整体无数据）

### 8.2 与 Filter 的关系

- `Filter`（`filter.go`）：底层 LLM 调用 + 批处理
- `ApplyFocusFilter`（`focus.go`）：编排层，负责数据准备与结果映射

---

## 9. 通用 AI 对话代理 `PostAIChat`

**文件:** `internal/api/handlers_chat.go`

### 9.1 数据流

1. HTTP `POST /api/v1/ai/chat` → 转发多轮消息到 LLM
2. 请求格式：`{ messages: [{role, content}], max_tokens?, temperature? }`
3. 安全限制：
   - 最多 48 条消息
   - 单条消息最长 16000 runes
   - 总长度 200000 runes
   - HTTP 超时 5 分钟
   - 输出 token 上限 16000
4. API key 不暴露给前端（后端持有）

### 9.2 配置

| 配置项 | 含义 |
|--------|------|
| `ai.model` / `ai.api_base` / `ai.api_key` | 共用 LLM 配置 |

---

## 10. 非 LLM 分析（关键词/统计算法）

**文件:** `internal/api/handlers.go`

以下 4 个分析端点**不使用 LLM**，基于关键词匹配和统计算法：

| Handler | 端点 | 方法 | 说明 |
|---------|------|------|------|
| `AnalyzeTopicTrend` | `POST /api/v1/analytics/topic/trend` | 关键词搜索 + 按日计数 | 输入话题关键词，按日期聚合新闻数量，支持 trend/lifecycle 模式 |
| `AnalyzeSentiment` | `POST /api/v1/analytics/sentiment` | 中文正负面关键词匹配 | 对标题做正面/负面关键词计数，返回分布与百分比 |
| `AggregateNews` | `POST /api/v1/analytics/aggregate` | Jaccard 相似度 + 关键词聚类 | 对标题分词后聚类，输出话题簇 |
| `ComparePeriods` | `POST /api/v1/analytics/compare-periods` | 关键词频率跨时段对比 | 输入两个日期范围，对比关键词出现次数变化，返回变化率 TOP 20 |

### 10.1 情感分析关键词

- **正面**: 利好、突破、增长、创新、上涨、夺冠、获奖、成功、领先、优化、升级……
- **负面**: 下跌、暴跌、崩盘、亏损、裁员、事故、危机、违规、处罚、失败、争议……

---

## 11. 行为对照表（便于排障）

| 现象 | 可能原因 |
|------|----------|
| 不过滤 | `filter.method` 非 `ai` 或 `interests` 为空 |
| 过滤超时 / 重试与全局不一致 | 过滤专用 30s 与单重试（见 2 节） |
| 解析 JSON 失败 | 模型未按数组或字段返回，或夹杂说明文字 |
| 分析总为空/未启用 | 接口未调用 `buildAINewsSummary`（当前设计） |
| 404/URL 错 | 检查 `api_base` 与 `/v1/chat/completions` 拼接（见 2 节） |

---

## 12. 相关源码索引

- `internal/ai/filter.go` — 兴趣过滤提示词与批处理
- `internal/ai/focus.go` — 焦点过滤编排层
- `internal/ai/analyzer.go` — 深度分析、摘要、情感、实体、分类
- `internal/ai/article_analyze.go` — 单条新闻文章摘要（正文抓取 + LLM）
- `internal/ai/day_industry.go` — 每日行业研报
- `internal/ai/client.go` — HTTP、Chat、AnalyzeNews
- `internal/ai/translator.go` — 翻译
- `internal/api/handlers.go` — 分析 handler（话题趋势、情感、聚类、时期对比） + AI 过滤 API
- `internal/api/handlers_chat.go` — 通用 AI 对话代理
- `internal/scheduler/scheduler.go` — `applyAIFocusFilter` 与邮件管线
- `pkg/config/config.go` — `FilterConfig`、`AIConfig`、`AIAnalysisConfig`、`AIFilterConfig`、`AITranslationConfig`
- `config/config.yaml` — 运行时可改参数（**勿将密钥提交公库**）

---

*实现变更时，请同步更新本文件。若未来将提示词外置为 `prompt_file`，建议在本文第 3、4 节增加「外置文件路径与占位符」说明。*
