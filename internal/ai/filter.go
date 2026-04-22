package ai

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Filter AI 过滤器
type Filter struct {
	client    *AIClient
	interests string // 用户兴趣描述
	minScore  float64 // 最小分数阈值
	batchSize int     // 批处理大小
}

// FilterResult 过滤结果
type FilterResult struct {
	Item   interface{} `json:"item"`   // 原始项
	Score  float64     `json:"score"`  // 兴趣匹配分数
	Reason string      `json:"reason"` // 过滤理由
	Tags   []string    `json:"tags"`   // 匹配的兴趣标签
}

// NewFilter 创建过滤器
func NewFilter(interests string, minScore float64, batchSize int) *Filter {
	if minScore <= 0 {
		minScore = 0.7
	}
	if batchSize <= 0 {
		batchSize = 200
	}

	return &Filter{
		client:    NewAIClient(),
		interests: interests,
		minScore:  minScore,
		batchSize: batchSize,
	}
}

// FilterNews 过滤新闻
func (f *Filter) FilterNews(newsItems []NewsItem) ([]FilterResult, error) {
	if len(newsItems) == 0 {
		return []FilterResult{}, nil
	}
	// AI 过滤场景使用更短超时和更少重试，避免接口长时间阻塞
	if f.client != nil {
		if f.client.timeout == 0 || f.client.timeout > 30*time.Second {
			f.client.timeout = 30 * time.Second
			f.client.client.Timeout = 30 * time.Second
		}
		if f.client.numRetries > 1 {
			f.client.numRetries = 1
		}
	}

	// 分批处理
	var allResults []FilterResult
	for i := 0; i < len(newsItems); i += f.batchSize {
		end := i + f.batchSize
		if end > len(newsItems) {
			end = len(newsItems)
		}

		batch := newsItems[i:end]
		results, err := f.filterBatch(batch)
		if err != nil {
			return nil, fmt.Errorf("failed to filter batch: %w", err)
		}

		allResults = append(allResults, results...)
	}

	return allResults, nil
}

// filterBatch 过滤一批新闻
func (f *Filter) filterBatch(newsItems []NewsItem) ([]FilterResult, error) {
	// 构建批处理请求
	var newsList strings.Builder
	newsList.WriteString("请分析以下新闻是否符合我的兴趣，并为每条新闻打分（0-1）：\n\n")
	newsList.WriteString("我的兴趣描述：\n")
	newsList.WriteString(f.interests)
	newsList.WriteString("\n\n新闻列表：\n")

	for i, item := range newsItems {
		rankStr := ""
		if item.Rank > 0 {
			rankStr = fmt.Sprintf("（排名:%d）", item.Rank)
		}
		newsList.WriteString(fmt.Sprintf("%d. %s%s\n", i+1, item.Title, rankStr))
	}

	newsList.WriteString("\n\n请按以下 JSON 格式返回结果：\n")
	newsList.WriteString("[\n")
	newsList.WriteString("  {\n")
	newsList.WriteString("    \"index\": 0, // 新闻索引（从 0 开始）\n")
	newsList.WriteString("    \"score\": 0-1 之间的分数，表示兴趣匹配度\n")
	newsList.WriteString("    \"reason\": \"简短的评分理由\",\n")
	newsList.WriteString("    \"tags\": [\"匹配的兴趣标签 1\", \"匹配的兴趣标签 2\"]\n")
	newsList.WriteString("  }\n")
	newsList.WriteString("]\n")

	// 调用 AI 模型
	messages := []ChatMessage{
		{
			Role:    "system",
			Content: "你是一个智能新闻过滤器，请根据用户的兴趣描述过滤新闻。",
		},
		{
			Role:    "user",
			Content: newsList.String(),
		},
	}

	response, err := f.client.Chat(messages)
	if err != nil {
		return nil, fmt.Errorf("AI chat failed: %w", err)
	}

	// 清理响应
	response = strings.TrimPrefix(response, "`")
	response = strings.TrimSuffix(response, "`")
	response = strings.TrimSpace(response)

	// 解析响应
	var rawResults []struct {
		Index  int    `json:"index"`
		Score  float64 `json:"score"`
		Reason string `json:"reason"`
		Tags   []string `json:"tags"`
	}

	if err := json.Unmarshal([]byte(response), &rawResults); err != nil {
		return nil, fmt.Errorf("failed to parse filter results: %w", err)
	}

	// 转换为 FilterResult
	results := make([]FilterResult, 0, len(rawResults))
	for _, raw := range rawResults {
		if raw.Index >= 0 && raw.Index < len(newsItems) {
			results = append(results, FilterResult{
				Item:  newsItems[raw.Index],
				Score: raw.Score,
				Reason: raw.Reason,
				Tags:  raw.Tags,
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

	// 转换为新闻项格式
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

// GetInterestedTags 获取感兴趣的话题标签
func (f *Filter) GetInterestedTags(text string) ([]string, error) {
	messages := []ChatMessage{
		{
			Role:    "system",
			Content: fmt.Sprintf("根据以下兴趣描述，分析文本涉及的话题标签：\n%s", f.interests),
		},
		{
			Role:    "user",
			Content: fmt.Sprintf("请为以下文本提取 3-5 个最相关的兴趣标签，返回 JSON 数组：\n\n%s", text),
		},
	}

	response, err := f.client.Chat(messages)
	if err != nil {
		return nil, err
	}

	var tags []string
	if err := json.Unmarshal([]byte(response), &tags); err != nil {
		return nil, fmt.Errorf("failed to parse tags: %w", err)
	}

	return tags, nil
}
