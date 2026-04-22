package ai

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Analyzer AI 分析器
type Analyzer struct {
	client *AIClient
}

// AnalysisConfig 分析配置
type AnalysisConfig struct {
	Mode          string   `json:"mode"`          // follow_report, daily, current, incremental
	Platforms     []string `json:"platforms"`     // 平台列表
	RssFeeds      []string `json:"rss_feeds"`     // RSS 源列表
	IncludeRss    bool     `json:"include_rss"`   // 是否包含 RSS
	MaxNews       int      `json:"max_news"`      // 最大新闻数量
	SummaryLength int      `json:"summary_length"` // 摘要长度
}

// AnalysisResult 分析结果
type AnalysisResult struct {
	CoreTrends         string            `json:"core_trends"`          // 核心趋势
	SentimentControversy string          `json:"sentiment_controversy"` // 情感与争议
	Signals            string            `json:"signals"`              // 信号与洞察
	RSSInsights        string            `json:"rss_insights"`        // RSS 洞察
	OutlookStrategy    string            `json:"outlook_strategy"`    // 展望与策略
	StandaloneSummaries map[string]string `json:"standalone_summaries"` // 独立摘要
	RawResponse        string            `json:"raw_response"`        // 原始响应
}

// NewAnalyzer 创建分析器
func NewAnalyzer() *Analyzer {
	return &Analyzer{
		client: NewAIClient(),
	}
}

// Analyze 执行 AI 分析
func (a *Analyzer) Analyze(config *AnalysisConfig, newsByPlatform map[string][]NewsItem, rssByFeed map[string][]RSSItem) (*AnalysisResult, error) {
	// 构建新闻摘要
	var newsSummary strings.Builder

	// 按平台分组新闻
	for platformID, items := range newsByPlatform {
		newsSummary.WriteString(fmt.Sprintf("\n【%s】\n", platformID))
		for i, item := range items {
			if i >= config.MaxNews {
				break
			}
			rankStr := ""
			if item.Rank > 0 {
				rankStr = fmt.Sprintf("(排名:%d)", item.Rank)
			}
			newsSummary.WriteString(fmt.Sprintf("  %d. %s%s\n", i+1, item.Title, rankStr))
		}
	}

	// 添加 RSS 新闻
	var rssSummary strings.Builder
	for feedID, items := range rssByFeed {
		rssSummary.WriteString(fmt.Sprintf("\n【%s】\n", feedID))
		for i, item := range items {
			if i >= 10 {
				break
			}
			rssSummary.WriteString(fmt.Sprintf("  %d. %s\n", i+1, item.Title))
		}
	}

	// 构建系统提示词
	systemPrompt := `你是一个专业的新闻分析专家，请根据以下新闻数据进行深度分析。` +
		`请从多个角度分析新闻趋势、热点话题、情感倾向等。`

	// 构建用户提示词
	var userPrompt strings.Builder
	userPrompt.WriteString("请分析以下新闻数据：\n\n")
	userPrompt.WriteString(newsSummary.String())

	if rssSummary.Len() > 0 {
		userPrompt.WriteString("\n\n--- RSS 新闻 ---\n\n")
		userPrompt.WriteString(rssSummary.String())
	}

	userPrompt.WriteString("\n\n请按以下格式输出分析结果（使用 JSON 格式）：\n")
	userPrompt.WriteString(`{
  "core_trends": "核心趋势分析：总结最重要的 3-5 个趋势，包含关键事件和人物",
  "sentiment_controversy": "情感与争议分析：分析整体情感倾向和可能的争议点",
  "signals": "信号与洞察：识别值得关注的信号和潜在趋势",
  "rss_insights": "RSS 洞察：如果有 RSS 新闻，分析其中的独特视角",
  "outlook_strategy": "展望与策略：对未来趋势的预判和建议",
  "standalone_summaries": {
    "summary": "一句话总结",
    "highlights": "3-5 个亮点"
  }
}`)

	// 调用 AI 模型
	messages := []ChatMessage{
		{
			Role:    "system",
			Content: systemPrompt,
		},
		{
			Role:    "user",
			Content: userPrompt.String(),
		},
	}

	rawResponse, err := a.client.Chat(messages)
	if err != nil {
		return nil, fmt.Errorf("AI chat failed: %w", err)
	}

	// 清理响应（去除可能的 markdown 标记）
	rawResponse = strings.TrimPrefix(rawResponse, "```json")
	rawResponse = strings.TrimSuffix(rawResponse, "```")
	rawResponse = strings.TrimSpace(rawResponse)

	// 解析 JSON 响应
	var result AnalysisResult
	if err := json.Unmarshal([]byte(rawResponse), &result); err != nil {
		// 如果解析失败，保存原始响应
		return &AnalysisResult{
			RawResponse: rawResponse,
		}, nil
	}

	result.RawResponse = rawResponse
	return &result, nil
}

// GenerateSummary 生成摘要
func (a *Analyzer) GenerateSummary(text string, maxTokens int) (string, error) {
	if maxTokens <= 0 {
		maxTokens = 200
	}

	messages := []ChatMessage{
		{
			Role:    "system",
			Content: "你是一个专业的新闻摘要生成器，请生成简洁、准确的摘要。",
		},
		{
			Role:    "user",
			Content: fmt.Sprintf("请为以下文本生成摘要（不超过%d个词）：\n\n%s", maxTokens, text),
		},
	}

	return a.client.Chat(messages)
}

// AnalyzeSentiment 分析情感倾向
func (a *Analyzer) AnalyzeSentiment(text string) (SentimentResult, error) {
	messages := []ChatMessage{
		{
			Role:    "system",
			Content: "你是一个情感分析专家，请分析文本的情感倾向。",
		},
		{
			Role:    "user",
			Content: fmt.Sprintf("请分析以下文本的情感倾向，返回 JSON 格式：{\"sentiment\": \"positive/negative/neutral\", \"confidence\": 0-1 之间的数值，\"reasoning\": \"分析理由\"}\n\n文本：%s", text),
		},
	}

	response, err := a.client.Chat(messages)
	if err != nil {
		return SentimentResult{}, err
	}

	var result SentimentResult
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		return SentimentResult{}, fmt.Errorf("failed to parse sentiment result: %w", err)
	}

	return result, nil
}

// SentimentResult 情感分析结果
type SentimentResult struct {
	Sentiment  string  `json:"sentiment"`
	Confidence float64 `json:"confidence"`
	Reasoning  string  `json:"reasoning"`
}

// ExtractEntities 提取实体
func (a *Analyzer) ExtractEntities(text string) ([]Entity, error) {
	messages := []ChatMessage{
		{
			Role:    "system",
			Content: "你是一个实体提取专家，请从文本中提取人名、地名、机构名等实体。",
		},
		{
			Role:    "user",
			Content: fmt.Sprintf("请从以下文本中提取实体，返回 JSON 数组：[{\"entity\": \"实体名\", \"type\": \"person/location/organization\"}]\n\n文本：%s", text),
		},
	}

	response, err := a.client.Chat(messages)
	if err != nil {
		return nil, err
	}

	var entities []Entity
	if err := json.Unmarshal([]byte(response), &entities); err != nil {
		return nil, fmt.Errorf("failed to parse entities: %w", err)
	}

	return entities, nil
}

// Entity 实体
type Entity struct {
	Entity string `json:"entity"`
	Type   string `json:"type"` // person, location, organization
}

// ClassifyTopic 分类话题
func (a *Analyzer) ClassifyTopic(text string, categories []string) (string, float64, error) {
	categoryList := strings.Join(categories, ", ")

	messages := []ChatMessage{
		{
			Role:    "system",
			Content: fmt.Sprintf("你是一个话题分类专家，请将文本分类到以下类别之一：%s", categoryList),
		},
		{
			Role:    "user",
			Content: fmt.Sprintf("请分类以下文本，返回 JSON 格式：{\"category\": \"类别名\", \"confidence\": 0-1 之间的数值}\n\n文本：%s", text),
		},
	}

	response, err := a.client.Chat(messages)
	if err != nil {
		return "", 0, err
	}

	var result struct {
		Category   string  `json:"category"`
		Confidence float64 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		return "", 0, fmt.Errorf("failed to parse classification result: %w", err)
	}

	return result.Category, result.Confidence, nil
}

// NewsItem 新闻项（简化版）
type NewsItem struct {
	Title  string `json:"title"`
	Rank   int    `json:"rank"`
	Source string `json:"source"`
}

// RSSItem RSS 项（简化版）
type RSSItem struct {
	Title string `json:"title"`
	Feed  string `json:"feed"`
}
