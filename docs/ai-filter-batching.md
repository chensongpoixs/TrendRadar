# AI 兴趣过滤：分批与业界实践

## 问题背景

- 热榜+RSS 合并后，一次请求里若包含**整份兴趣长文** + **上百条标题**，会导致：
  - **Prompt 过大**：延迟高、易超时、本机 Ollama/GPU 吃紧。
  - **输出 JSON 过长**：`max_tokens` 在边界被**截断**，`message.content` 变空或非法 JSON，解析失败。

本实现采用**串行多批**（**Map-Reduce 的简化版**）：每批独请求，**本地合并** `FilterResult`，不依赖模型跨批记忆。

## 本后端策略（`internal/ai/filter.go`）

| 配置项 | 含义 |
|--------|------|
| `ai_filter.batch_size` | 每批**最多**标题条数。 |
| `ai_filter.max_input_chars` | 单批 `user` 正文的 **Unicode 码点**数上限（含兴趣全文、标题列表、模板尾）。`0` 表示**不**做体量切分，只按 `batch_size` 切。两约束**同时**满足：先累加条数，直到超出条数或超出字符上限。 |
| `ai_filter.batch_interval` | 批次间 **sleep（毫秒）**，减轻本机推理排队与简单限流；`0` 不等待。 |
| `ai_filter.max_output_tokens` | 仅作用于**兴趣过滤**请求的 `max_tokens`；`0` 表示沿用全局 `ai.max_tokens`。JSON 任务建议**单独设足**，避免输出被顶满。 |
| `ai_filter.min_score` | 见既有文档。 |

HTTP 方面：兴趣过滤内仍把重试压为 1 次；超时若客户端未设则**继承 `ai.timeout`（秒）**。

## 业界常见做法（对照）

1. **固定条数/固定窗口**（本实现的 `batch_size`）：实现简单、可预期。
2. **按 token/字符预算切批**（本实现的 `max_input_chars` 近似字符侧）：与 LangChain “token splitter”、OpenAI 长文分段策略同思路；精确控制需 `tiktoken`/本地分词，此处用 **rune 计数** 零依赖、对中文更稳 than raw bytes。
3. **批间退避/限速**（`batch_interval`）：类客户端 **rate limit**、Ollama 多并发时的 **队列减压**。
4. **按任务独立 `max_output_tokens`**：避免与其它用途共用全局 `max_tokens` 导致**过滤**输出不够长。

**未做**（若以后要扩展）：多批**并行**、跨批去重、失败批次单独重试；当前以**可预测与易排错**为主。

## 调参建议

- Ollam 上小模型/CPU：**减小** `batch_size` 与 `max_input_chars`，适当**增大** `batch_interval`。
- 仍报「content 空 / JSON 被截断」：优先**提高** `max_output_tokens`，再**减小**单批条数或 `max_input_chars`。
- 与仓库根 `config/ai_interests.txt` 同步缩短兴趣描述，也能直接**降低**单批压力。

## 配置位置

- YAML 节名：**`ai_filter`**
- 环境变量（Viper）：`TRENDRADAR_AI_FILTER_*` 等形式（以实际绑定为准）
