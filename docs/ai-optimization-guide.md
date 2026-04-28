# AI 数据优化指南

本文档介绍 TrendRadar Go 后端 AI 相关功能的优化方案，旨在提升性能、降低 Token 消耗、减少响应延迟。

## 目录

- [现状分析](#现状分析)
- [优化点清单](#优化点清单)
- [详细优化方案](#详细优化方案)
- [性能预估](#性能预估)
- [实施计划](#实施计划)
- [监控指标](#监控指标)

---

## 现状分析

### 当前 AI 架构

```
┌─────────────┐     ┌──────────────┐     ┌─────────────┐
│   MCP/API   │────>│  AI Client   │────>│  LLM API    │
│  Handlers   │     │ (Blocking)   │     │  Provider   │
└─────────────┘     └──────────────┘     └─────────────┘
                          │
                    ┌─────┴─────┐
                    │ Concurrency │
                    │  Control    │
                    └─────────────┘
```

**当前问题：**
1. AI 调用是同步阻塞的，没有缓存层
2. 并发控制使用简单 semaphore，没有自适应机制
3. 每日报告生成时串行处理所有新闻，耗时长
4. 没有智能模型路由，所有任务使用同一模型
5. 没有响应缓存，相同内容反复调用 API

---

## 优化点清单

### P0 高优先级

| # | 优化点 | 预期收益 | 实现难度 |
|---|--------|----------|----------|
| 1 | AI 响应缓存 | 50-99% 延迟降低 | 中 |
| 2 | 并发控制优化 | 30-50% 吞吐量提升 | 低 |
| 3 | 异步报告生成 | 100% 非阻塞 | 高 |

### P1 中优先级

| # | 优化点 | 预期收益 | 实现难度 |
|---|--------|----------|----------|
| 4 | 智能模型路由 | 37-50% Token 节省 | 中 |
| 5 | 批量处理优化 | 20-40% 成本降低 | 中 |
| 6 | 流式响应 | 用户体验提升 | 高 |
| 7 | 请求去重 | 10-30% API 调用减少 | 低 |

### P2 低优先级

| # | 优化点 | 预期收益 | 实现难度 |
|---|--------|----------|----------|
| 8 | 增量分析 | 60-80% Token 节省 | 中 |
| 9 | 本地缓存分析 | 90%+ 延迟降低 | 高 |
| 10 | 预计算热点数据 | 实时查询 | 中 |

---

## 详细优化方案

### 1. AI 响应缓存层

**问题：** 相同标题/话题反复调用 AI，浪费 Token

**解决方案：** 基于内容哈希的 LRU 缓存

```go
// internal/ai/cache.go
package ai

import (
    "crypto/sha256"
    "time"
    "github.com/patrickmn/go-cache"
)

type CacheEntry struct {
    Hash      string    `json:"hash"`
    Response  string    `json:"response"`
    Tokens    int       `json:"tokens"`
    Model     string    `json:"model"`
    CreatedAt time.Time `json:"created_at"`
}

type ResponseCache struct {
    cache *cache.Cache
    hits  int64
    misses int64
}

func NewResponseCache(defaultTTL time.Duration) *ResponseCache {
    return &ResponseCache{
        cache: cache.New(defaultTTL, 10*time.Minute),
    }
}

func (c *ResponseCache) ContentHash(input string) string {
    h := sha256.New()
    h.Write([]byte(input))
    return fmt.Sprintf("%x", h.Sum(nil))
}

func (c *ResponseCache) Get(hash, model string) (string, bool) {
    entry, found := c.cache.Get(hash + ":" + model)
    if !found {
        c.misses++
        return "", false
    }
    c.hits++
    return entry.(*CacheEntry).Response, true
}

func (c *ResponseCache) Set(hash, model, response string, tokens int) {
    c.cache.Set(hash+":"+model, &CacheEntry{
        Hash:      hash,
        Response:  response,
        Tokens:    tokens,
        Model:     model,
        CreatedAt: time.Now(),
    }, cache.DefaultExpiration)
}

func (c *ResponseCache) Stats() map[string]interface{} {
    total := c.hits + c.misses
    hitRate := 0.0
    if total > 0 {
        hitRate = float64(c.hits) / float64(total) * 100
    }
    return map[string]interface{}{
        "hits":    c.hits,
        "misses":  c.misses,
        "rate":    fmt.Sprintf("%.1f%%", hitRate),
        "items":   c.cache.ItemCount(),
    }
}
```

**配置示例：**
```yaml
ai:
  cache:
    enabled: true
    default_ttl: 24h
    max_size: 10000
    models:
      - "deepseek/deepseek-chat"
      - "anthropic/claude-3.5-sonnet"
```

---

### 2. 并发控制优化

**问题：** 固定 semaphore 无法适应不同 API 的速率限制

**解决方案：** 基于令牌桶的自适应并发控制

```go
// internal/ai/concurrency.go
package ai

import (
    "github.com/juju/ratelimit"
    "sync"
)

type ProviderRateLimit struct {
    RequestsPerMinute int
    TokensPerRequest  int
    bucket            *ratelimit.Bucket
    mu                sync.Mutex
}

type ConcurrencyController struct {
    providers map[string]*ProviderRateLimit
    global    *ratelimit.Bucket
}

func NewConcurrencyController(config map[string]int) *ConcurrencyController {
    cc := &ConcurrencyController{
        providers: make(map[string]*ProviderRateLimit),
    }

    // 初始化各 provider 限流
    for provider, rpm := range config {
        tb := ratelimit.NewBucketWithQuantum(
            time.Duration(rpm)*time.Minute,
            time.Duration(rpm)*time.Minute,
            ratelimit.Units(rpm),
        )
        cc.providers[provider] = &ProviderRateLimit{
            RequestsPerMinute: rpm,
            bucket:            tb,
        }
    }

    // 全局并发限制
    cc.global = ratelimit.NewBucket(time.Second, 50)
    return cc
}

func (cc *ConcurrencyController) Acquire(provider string) error {
    // 获取 provider 级别的令牌
    if pl, ok := cc.providers[provider]; ok {
        pl.mu.Lock()
        defer pl.mu.Unlock()
        if !pl.bucket.Take(1) {
            return fmt.Errorf("rate limit exceeded for provider: %s", provider)
        }
    }

    // 获取全局并发令牌
    if !cc.global.Take(1) {
        time.Sleep(100 * time.Millisecond)
        return cc.Acquire(provider)
    }
    return nil
}
```

---

### 3. 异步报告生成

**问题：** 每日报告生成串行处理，阻塞 HTTP 请求

**解决方案：** 异步 Job 队列 + WebSocket 进度推送

```go
// internal/ai/async_report.go
package ai

import (
    "context"
    "sync"
    "time"
)

type ReportJob struct {
    ID         string
    NewsItems  []NewsItem
    Progress   int
    Total      int
    Result     string
    Error      error
    Status     string // pending, running, completed, failed
    CreatedAt  time.Time
    UpdatedAt  time.Time
}

type AsyncReportManager struct {
    jobs    map[string]*ReportJob
    queue   chan *ReportJob
    workers int
    wg      sync.WaitGroup
}

func NewAsyncReportManager(workers int) *AsyncReportManager {
    arm := &AsyncReportManager{
        jobs:    make(map[string]*ReportJob),
        queue:   make(chan *ReportJob, 100),
        workers: workers,
    }
    arm.startWorkers()
    return arm
}

func (arm *AsyncReportManager) SubmitJob(job *ReportJob) string {
    job.ID = generateID()
    job.Status = "pending"
    job.CreatedAt = time.Now()
    arm.jobs[job.ID] = job
    arm.queue <- job
    return job.ID
}

func (arm *AsyncReportManager) startWorkers() {
    for i := 0; i < arm.workers; i++ {
        arm.wg.Add(1)
        go arm.worker()
    }
}

func (arm *AsyncReportManager) worker() {
    defer arm.wg.Done()
    for job := range arm.queue {
        arm.processJob(job)
    }
}

func (arm *AsyncReportManager) processJob(job *ReportJob) {
    job.Status = "running"
    job.UpdatedAt = time.Now()

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
    defer cancel()

    // 分批处理新闻（每批 50 条）
    batchSize := 50
    results := make([]string, 0)

    for i := 0; i < len(job.NewsItems); i += batchSize {
        end := i + batchSize
        if end > len(job.NewsItems) {
            end = len(job.NewsItems)
        }

        batch := job.NewsItems[i:end]
        result, err := arm.analyzeBatch(ctx, batch)
        if err != nil {
            job.Error = err
            job.Status = "failed"
            return
        }
        results = append(results, result)

        // 更新进度
        job.Progress = int(float64(i) / float64(len(job.NewsItems)) * 100)
        job.UpdatedAt = time.Now()
    }

    job.Result = strings.Join(results, "\n")
    job.Status = "completed"
    job.Progress = 100
    job.UpdatedAt = time.Now()
}

func (arm *AsyncReportManager) GetJobStatus(jobID string) (*ReportJob, error) {
    job, ok := arm.jobs[jobID]
    if !ok {
        return nil, fmt.Errorf("job not found: %s", jobID)
    }
    return job, nil
}
```

---

### 4. 智能模型路由

**问题：** 所有任务使用同一模型，简单任务浪费高级模型资源

**解决方案：** 基于任务类型和复杂度的模型自动选择

```go
// internal/ai/router.go
package ai

type TaskType string

const (
    TaskTypeSummary    TaskType = "summary"
    TaskTypeAnalysis   TaskType = "analysis"
    TaskTypeSentiment  TaskType = "sentiment"
    TaskTypeTranslation TaskType = "translation"
    TaskTypeFiltering  TaskType = "filtering"
)

type ModelRouter struct {
    models map[TaskType]string
    costs  map[string]float64 // $ per 1M tokens
}

func NewModelRouter() *ModelRouter {
    return &ModelRouter{
        models: map[TaskType]string{
            TaskTypeSummary:    "deepseek/deepseek-chat",      // 便宜，适合摘要
            TaskTypeAnalysis:   "anthropic/claude-3.5-sonnet", // 复杂分析用强模型
            TaskTypeSentiment:  "deepseek/deepseek-chat",      // 情感分析简单
            TaskTypeTranslation: "anthropic/claude-3.5-sonnet", // 翻译需要高质量
            TaskTypeFiltering:  "deepseek/deepseek-chat",      // 过滤简单任务
        },
        costs: map[string]float64{
            "deepseek/deepseek-chat":         0.001,  // $1/1M tokens (input)
            "anthropic/claude-3.5-sonnet":    3.0,    // $3/1M tokens (input)
        },
    }
}

func (r *ModelRouter) SelectModel(taskType TaskType) string {
    if model, ok := r.models[taskType]; ok {
        return model
    }
    return r.models[TaskTypeAnalysis] // 默认使用分析模型
}

func (r *ModelRouter) EstimateCost(taskType TaskType, estimatedTokens int) float64 {
    model := r.SelectModel(taskType)
    costPerToken := r.costs[model] / 1000000.0
    return float64(estimatedTokens) * costPerToken
}
```

**成本对比：**
```
任务类型       原模型              优化后模型           节省
─────────────────────────────────────────────────────────
摘要生成       claude-3.5-sonnet    deepseek-chat       99.97%
情感分析       claude-3.5-sonnet    deepseek-chat       99.97%
复杂分析       claude-3.5-sonnet    claude-3.5-sonnet   0%
翻译           claude-3.5-sonnet    claude-3.5-sonnet   0%
新闻过滤       claude-3.5-sonnet    deepseek-chat       99.97%

预估月度节省: 约 $50-100 (基于 10 万次 AI 调用)
```

---

### 5. 批量处理优化

**问题：** 每条新闻单独调用 AI 进行过滤/分析

**解决方案：** 相似请求合并，一次 API 调用处理多条

```go
// internal/ai/batch_processor.go
package ai

import (
    "context"
    "strings"
    "time"
)

type BatchRequest struct {
    Items     []AIItem
    Prompt    string
    TaskType  TaskType
    MaxItems  int
}

type BatchResult struct {
    Responses []string
    TotalTokens int
    Model     string
}

type BatchProcessor struct {
    client  *AIClient
    maxSize int
    timeout time.Duration
}

func NewBatchProcessor(client *AIClient, maxSize int, timeout time.Duration) *BatchProcessor {
    return &BatchProcessor{
        client:  client,
        maxSize: maxSize,
        timeout: timeout,
    }
}

func (bp *BatchProcessor) Process(ctx context.Context, req *BatchRequest) (*BatchResult, error) {
    results := &BatchResult{}
    totalItems := len(req.Items)

    // 分批处理
    for i := 0; i < totalItems; i += bp.maxSize {
        end := i + bp.maxSize
        if end > totalItems {
            end = totalItems
        }

        batch := req.Items[i:end]
        batchPrompt := bp.buildBatchPrompt(req.Prompt, batch)

        response, tokens, err := bp.client.Call(ctx, bp.SelectModel(req.TaskType), batchPrompt)
        if err != nil {
            return nil, err
        }

        // 解析批量响应
        responses := bp.parseBatchResponse(response)
        results.Responses = append(results.Responses, responses...)
        results.TotalTokens += tokens
    }

    return results, nil
}

func (bp *BatchProcessor) buildBatchPrompt(basePrompt string, items []AIItem) string {
    var sb strings.Builder
    sb.WriteString(basePrompt)
    sb.WriteString("\n\n请分析以下新闻条目：\n\n")

    for i, item := range items {
        sb.WriteString(fmt.Sprintf("%d. %s - %s\n", i+1, item.Title, item.Summary))
    }

    sb.WriteString("\n\n请为每条新闻返回分析结果，使用格式：\n1. [结果]\n2. [结果]\n...")
    return sb.String()
}

func (bp *BatchProcessor) parseBatchResponse(response string) []string {
    // 按数字行号分割响应
    lines := strings.Split(response, "\n")
    results := make([]string, 0)

    for _, line := range lines {
        if strings.HasPrefix(line, "[0-9]+.") {
            results = append(results, strings.TrimSpace(line))
        }
    }
    return results
}
```

---

## 性能预估

### 基准测试数据

| 指标 | 优化前 | 优化后 | 提升 |
|------|--------|--------|------|
| AI 缓存命中率 | 0% | 60-80% | - |
| 摘要生成延迟 | 3-5s | 0-50ms (缓存命中) | 99%+ |
| 每日报告生成 | 120s (串行) | 30s (并行) | 75% |
| Token 消耗/天 | 500K | 250K-350K | 30-50% |
| 并发处理能力 | 10 req/s | 25-50 req/s | 150-400% |
| 月度 API 成本 | $100-150 | $50-100 | 33-50% |

### 场景测试

**场景 1：相同话题重复查询**
```
优化前: 5 次独立 API 调用 = 5 × 3s = 15s
优化后: 1 次 API 调用 + 4 次缓存 = 3s + 0s × 4 = 3s
节省: 80% 时间, 80% Token
```

**场景 2：每日报告生成**
```
优化前: 1000 条新闻 × 3s = 3000s (串行)
优化后: 1000/50 批次 × 3s/5 = 60s (5 并发)
节省: 98% 时间
```

**场景 3：AI 过滤批量处理**
```
优化前: 200 条 × 1s = 200s, 200K tokens
优化后: 200/50 批次 × 1s/5 = 20s, 100K tokens
节省: 90% 时间, 50% Token
```

---

## 实施计划

### 阶段一：缓存与并发（第 1-2 周）

**任务清单：**
- [ ] 实现 ResponseCache 结构
- [ ] 添加缓存配置支持
- [ ] 集成缓存到 AI Client
- [ ] 实现 ConcurrencyController
- [ ] 添加 Prometheus 指标
- [ ] 编写单元测试

**验收标准：**
- 缓存命中率 ≥ 60%
- 缓存 API 调用延迟 ≤ 10ms
- 并发控制无死锁

---

### 阶段二：异步报告（第 2-3 周）

**任务清单：**
- [ ] 实现 AsyncReportManager
- [ ] 添加 WebSocket 进度推送
- [ ] 实现 Job 状态查询 API
- [ ] 添加失败重试机制
- [ ] 编写集成测试

**验收标准：**
- 报告生成不阻塞 HTTP 请求
- 进度更新延迟 ≤ 1s
- 失败任务自动重试 3 次

---

### 阶段三：模型路由（第 3-4 周）

**任务清单：**
- [ ] 实现 ModelRouter
- [ ] 添加任务类型识别
- [ ] 集成到 AI Client
- [ ] 添加成本统计
- [ ] 编写单元测试

**验收标准：**
- 简单任务使用低成本模型
- 成本估算误差 ≤ 10%
- 不影响分析质量

---

### 阶段四：批量处理（第 4-5 周）

**任务清单：**
- [ ] 实现 BatchProcessor
- [ ] 添加请求去重
- [ ] 实现增量分析
- [ ] 添加性能监控
- [ ] 端到端测试

**验收标准：**
- 批量处理效率 ≥ 4x
- 请求去重率 ≥ 20%
- Token 节省 ≥ 30%

---

## 监控指标

### Prometheus Metrics

```go
// internal/ai/metrics.go
package ai

import "github.com/prometheus/client_golang/prometheus"

var (
    // 缓存指标
    CacheHits = prometheus.NewCounter(prometheus.CounterOpts{
        Name: "ai_cache_hits_total",
        Help: "Total cache hits",
    })
    CacheMisses = prometheus.NewCounter(prometheus.CounterOpts{
        Name: "ai_cache_misses_total",
        Help: "Total cache misses",
    })

    // Token 消耗
    TokenConsumption = prometheus.NewCounter(prometheus.CounterOpts{
        Name: "ai_tokens_consumed_total",
        Help: "Total tokens consumed",
    })
    TokenByModel = prometheus.NewGaugeVec(prometheus.GaugeOpts{
        Name: "ai_tokens_by_model",
        Help: "Tokens consumed by model",
    }, []string{"model"})

    // 请求延迟
    AIDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
        Name:    "ai_request_duration_seconds",
        Help:    "AI request duration",
        Buckets: prometheus.DefBuckets,
    }, []string{"model", "task_type", "cached"})

    // 错误率
    AIErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
        Name: "ai_errors_total",
        Help: "Total AI errors",
    }, []string{"model", "error_type"})

    // 成本统计
    AICost = prometheus.NewGaugeVec(prometheus.GaugeOpts{
        Name: "ai_cost_dollars",
        Help: "AI API cost in dollars",
    }, []string{"model"})
)
```

### Grafana Dashboard 建议

```json
{
  "dashboard": {
    "title": "AI Performance Overview",
    "panels": [
      {
        "title": "Cache Hit Rate",
        "expr": "rate(ai_cache_hits_total[5m]) / (rate(ai_cache_hits_total[5m]) + rate(ai_cache_misses_total[5m])) * 100"
      },
      {
        "title": "Average AI Latency",
        "expr": "histogram_quantile(0.95, rate(ai_request_duration_seconds_bucket[5m]))"
      },
      {
        "title": "Token Consumption by Model",
        "expr": "rate(ai_tokens_consumed_total[1h])"
      },
      {
        "title": "Estimated Daily Cost",
        "expr": "sum(ai_cost_dollars) * 24"
      }
    ]
  }
}
```

---

## 快速开始

### 1. 启用缓存

```bash
# 环境变量配置
export TRENDRADAR_AI_CACHE_ENABLED=true
export TRENDRADAR_AI_CACHE_TTL=24h
export TRENDRADAR_AI_CACHE_MAX_SIZE=10000
```

### 2. 启用模型路由

```yaml
# config/config.yaml
ai:
  routing:
    enabled: true
    task_models:
      summary: "deepseek/deepseek-chat"
      analysis: "anthropic/claude-3.5-sonnet"
      sentiment: "deepseek/deepseek-chat"
      translation: "anthropic/claude-3.5-sonnet"
```

### 3. 启用异步报告

```bash
# 环境变量配置
export TRENDRADAR_AI_ASYNC_REPORT_ENABLED=true
export TRENDRADAR_AI_ASYNC_REPORT_WORKERS=5
```

---

## 回滚方案

如果优化出现问题，可以通过以下配置禁用：

```yaml
ai:
  cache:
    enabled: false
  routing:
    enabled: false
  async_report:
    enabled: false
  batch:
    enabled: false

# 回退到默认模型
ai:
  model: "anthropic/claude-3.5-sonnet"
```

---

## 联系与支持

如有问题或建议，请通过以下方式联系：
- GitHub Issues: https://github.com/trendradar/backend-go/issues
- 文档目录: `docs/`
