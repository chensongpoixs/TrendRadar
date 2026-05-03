package ai

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/trendradar/backend-go/pkg/config"
	"github.com/trendradar/backend-go/pkg/logger"
	"go.uber.org/zap"
)

// FilterOptions 控制兴趣过滤的分批策略（与 config ai_filter 对齐）。
type FilterOptions struct {
	BatchSize       int // 每批最多条数
	MaxInputChars   int // 单批 user 内容约上限（rune）；0=仅按 BatchSize
	BatchIntervalMS int // 批次间 sleep，缓解本机 Ollama/限流
	MaxOutputTokens int // 单请求 max_tokens 覆盖；0=用全局 ai.max_tokens
}

// Filter AI 过滤器
type Filter struct {
	client          *AIClient
	interests       string
	minScore        float64
	batchSize       int
	maxInputChars   int
	batchIntervalMS int
	maxOutputTokens int
}

// FilterResult 过滤结果
type FilterResult struct {
	Item   NewsItem `json:"item"`
	Score  float64  `json:"score"`
	Reason string   `json:"reason"`
	Tags   []string `json:"tags"`
}

// NewFilter 创建过滤器（minScore≤0 时内部用 0.7）
func NewFilter(interests string, minScore float64, opt FilterOptions) *Filter {
	if minScore <= 0 {
		minScore = 0.7
	}
	bs := opt.BatchSize
	if bs <= 0 {
		bs = 20
	}
	return &Filter{
		client:          NewAIClientFromConfig(config.Get().AIFilter.EffectiveAIConfig(config.Get().AI)),
		interests:       interests,
		minScore:        minScore,
		batchSize:       bs,
		maxInputChars:   opt.MaxInputChars,
		batchIntervalMS: opt.BatchIntervalMS,
		maxOutputTokens: opt.MaxOutputTokens,
	}
}

// FilterNews 过滤新闻：按条数、按单批输入体量（max_input_chars）切块，多批串行合并。
func (f *Filter) FilterNews(newsItems []NewsItem) ([]FilterResult, error) {
	if len(newsItems) == 0 {
		return []FilterResult{}, nil
	}
	if f.client != nil {
		applyAIFilterHTTPDefaults(f.client)
	}

	var allResults []FilterResult
	batchIdx := 0
	for start := 0; start < len(newsItems); {
		batch, next := f.nextBatchSlice(newsItems, start)
		if len(batch) == 0 {
			break
		}
		batchIdx++
		logger.WithComponent("aifilter").Info("aifilter batch",
			zap.Int("batch_no", batchIdx), zap.Int("items", len(batch)), zap.Int("start", start),
			zap.Int("max_size", f.batchSize), zap.Int("max_input_runes", f.maxInputChars))

		results, err := f.filterBatch(batch)
		if err != nil {
			return nil, fmt.Errorf("failed to filter batch: %w", err)
		}
		allResults = append(allResults, results...)

		start = next
		if start < len(newsItems) && f.batchIntervalMS > 0 {
			time.Sleep(time.Duration(f.batchIntervalMS) * time.Millisecond)
		}
	}

	return allResults, nil
}

// nextBatchSlice 在 [start:] 上取下一批：受 batch_size 与 max_input_chars（rune 计量）共同约束。
func (f *Filter) nextBatchSlice(all []NewsItem, start int) ([]NewsItem, int) {
	if start >= len(all) {
		return nil, start
	}
	if f.maxInputChars <= 0 {
		end := start + f.batchSize
		if end > len(all) {
			end = len(all)
		}
		return all[start:end], end
	}
	var batch []NewsItem
	for j := start; j < len(all) && len(batch) < f.batchSize; j++ {
		cand := append(append([]NewsItem{}, batch...), all[j])
		if utf8.RuneCountInString(f.buildUserContent(cand)) > f.maxInputChars {
			if len(batch) == 0 {
				// 单条超上限仍发送一条，避免死循环
				return all[j : j+1], j+1
			}
			break
		}
		batch = cand
	}
	if len(batch) == 0 {
		return all[start : start+1], start + 1
	}
	return batch, start + len(batch)
}

// buildUserContent 与 filterBatch 中 user 报文一致，供体量估算
func (f *Filter) buildUserContent(newsItems []NewsItem) string {
	var b strings.Builder
	b.WriteString("请分析以下新闻是否符合我的兴趣，并为每条新闻打分（0-1）。\n\n")
	b.WriteString("我的兴趣描述：\n")
	b.WriteString(f.interests)
	b.WriteString("\n\n新闻列表：\n")
	for i, item := range newsItems {
		rankStr := ""
		if item.Rank > 0 {
			rankStr = fmt.Sprintf("（排名:%d）", item.Rank)
		}
		b.WriteString(fmt.Sprintf("%d. %s%s\n", i+1, item.Title, rankStr))
	}
	b.WriteString("\n\n请为上述每条新闻返回一个 JSON 数组，示例（2 条时）：\n")
	b.WriteString(`[{"index":0,"score":0.85,"reason":"涉及AI模型发布","tags":["大模型"]},{"index":1,"score":0.2,"reason":"与兴趣无关","tags":[]}]`)
	b.WriteString("\n\n字段说明：index 从 0 开始对应新闻序号，score 为 0-1 浮点数，reason 简短理由，tags 为匹配的兴趣标签数组。\n")
	return b.String()
}

// applyAIFilterHTTPDefaults 兴趣过滤单请求：沿用全局 AI 超时与重试，若未设超时则用配置 ai.timeout
func applyAIFilterHTTPDefaults(c *AIClient) {
	if c == nil {
		return
	}
	cfg := config.Get()
	if cfg == nil {
		return
	}
	// 重试：避免过滤拖死队列
	if c.numRetries > 1 {
		c.numRetries = 1
	}
	// 超时：未设置时用 yaml ai.timeout（秒），仍 0 则 120s
	if c.timeout == 0 {
		tsec := cfg.AI.Timeout
		if tsec <= 0 {
			tsec = 120
		}
		c.timeout = time.Duration(tsec) * time.Second
		c.client.Timeout = c.timeout
	}
}

// filterBatch 过滤一批新闻
func (f *Filter) filterBatch(newsItems []NewsItem) ([]FilterResult, error) {
	start := time.Now()
	userContent := f.buildUserContent(newsItems)

	messages := []ChatMessage{
		{
			Role:    "system",
			Content: "你是科技产业信息过滤专家，服务于投资人、产品经理与技术决策者。你的任务是判断一条新闻标题是否对行业从业者有信息增量——区分「值得花时间深读的信号」与「泛娱乐/纯情绪/无信息量的噪声」。请严格只返回纯 JSON 数组，不要包含 markdown 标记、代码围栏或任何解释文字。",
		},
		{
			Role:    "user",
			Content: userContent,
		},
	}

	var response string
	var err error
	if f.maxOutputTokens > 0 {
		response, err = f.client.ChatWithMaxOutput(messages, f.maxOutputTokens)
	} else {
		response, err = f.client.Chat(messages)
	}
	if err != nil {
		return nil, fmt.Errorf("AI chat failed: %w", err)
	}

	type filterRaw struct {
		Index  int      `json:"index"`
		Score  float64  `json:"score"`
		Reason string   `json:"reason"`
		Tags   []string `json:"tags"`
	}
	rawResults, err := unmarshalFromLLM[[]filterRaw](response)
	if err != nil {
		return nil, fmt.Errorf("failed to parse filter results: %w", err)
	}

	if len(rawResults) != len(newsItems) {
		logger.WithComponent("aifilter").Warn("api row count mismatch",
			zap.Int("expected", len(newsItems)), zap.Int("got", len(rawResults)))
	}
	logger.WithComponent("aifilter").Info("aifilter api batch done",
		zap.Int("size", len(newsItems)), zap.Int("parsed", len(rawResults)),
		zap.Int64("duration_ms", time.Since(start).Milliseconds()))

	results := make([]FilterResult, 0, len(rawResults))
	for _, raw := range rawResults {
		if raw.Index >= 0 && raw.Index < len(newsItems) {
			results = append(results, FilterResult{
				Item:   newsItems[raw.Index],
				Score:  raw.Score,
				Reason: raw.Reason,
				Tags:   raw.Tags,
			})
		}
	}

	return results, nil
}

// FilterRSS 过滤 RSS
func (f *Filter) FilterRSS(rssItems []RSSItem) ([]FilterResult, error) {
	if len(rssItems) == 0 {
		return []FilterResult{}, nil
	}
	newsItems := make([]NewsItem, len(rssItems))
	for i, item := range rssItems {
		newsItems[i] = NewsItem{
			Title:  item.Title,
			Rank:   0,
			Source: item.Feed,
		}
	}
	return f.FilterNews(newsItems)
}

// GetFilteredItems 获取过滤后的项（分数高于阈值）
func GetFilteredItems(results []FilterResult, minScore float64) []FilterResult {
	filtered := make([]FilterResult, 0)
	for _, result := range results {
		if result.Score >= minScore {
			filtered = append(filtered, result)
		}
	}
	return filtered
}
