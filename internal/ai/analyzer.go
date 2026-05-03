package ai

import (
	"fmt"
	"strings"

	"github.com/trendradar/backend-go/pkg/config"
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
	cfg := config.Get()
	return &Analyzer{
		client: NewAIClientFromConfig(cfg.AIAnalysis.EffectiveAIConfig(cfg.AI)),
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

	systemPrompt := `你是资深科技产业与一二级投研背景的行业研究专家。你的分析面向投资人、创业者、产品经理与企业战略决策者，需要兼顾产业深度与商业落地性。` +
		`请严格只返回纯 JSON 对象，不要包含 markdown 标记、代码围栏或任何解释文字。` +
		`分析时遵循以下原则：` +
		`1) 产业链视角：将碎片化新闻串联为产业上下游信号，识别关键技术路线、竞争格局变化与供应链动向；` +
		`2) 资本敏感度：关注投融资/并购/IPO/政策变动对赛道估值的影响，区分"情绪炒作"与"基本面变化"；` +
		`3) 可执行洞察：输出的结论应指向"下一步可以做什么"——关注什么赛道、跟踪什么公司、学习什么技术；` +
		`4) 风险辨识：识别过度炒作、政策风险、技术成熟度陷阱等潜在利空。`

	// 构建用户提示词
	var userPrompt strings.Builder
	userPrompt.WriteString("请分析以下新闻数据：\n\n")
	userPrompt.WriteString(newsSummary.String())

	if rssSummary.Len() > 0 {
		userPrompt.WriteString("\n\n--- RSS 新闻 ---\n\n")
		userPrompt.WriteString(rssSummary.String())
	}

	userPrompt.WriteString("\n\n请返回如下 JSON 对象（所有值为字符串），示例：\n")
	userPrompt.WriteString(`{"core_trends":"最重要的3-5个趋势，含关键事件和人物","sentiment_controversy":"情感倾向和争议点","signals":"值得关注的信号与潜在趋势","rss_insights":"RSS中的独特视角（无RSS则写暂无）","outlook_strategy":"对未来趋势的预判和建议","standalone_summaries":{"summary":"一句话总结","highlights":"3-5个亮点"}}`)
	userPrompt.WriteString("\n")

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

	result, err := unmarshalFromLLM[AnalysisResult](rawResponse)
	if err != nil {
		// 解析失败时保存原始响应，不阻断流程
		return &AnalysisResult{RawResponse: rawResponse}, nil
	}
	result.RawResponse = rawResponse
	return &result, nil
}

// AnalyzeSentiment 分析情感倾向
func (a *Analyzer) AnalyzeSentiment(text string) (SentimentResult, error) {
	messages := []ChatMessage{
		{
			Role:    "system",
			Content: "你是行业舆论分析专家，擅长从市场情绪中识别过度乐观/恐慌信号与认知偏差。请严格只返回纯 JSON，不要包含 markdown 或解释文字。",
		},
		{
			Role:    "user",
			Content: fmt.Sprintf("请分析以下文本的情感倾向，返回示例：{\"sentiment\":\"positive\",\"confidence\":0.82,\"reasoning\":\"理由\"}\n\n文本：%s", text),
		},
	}

	response, err := a.client.Chat(messages)
	if err != nil {
		return SentimentResult{}, err
	}

	result, err := unmarshalFromLLM[SentimentResult](response)
	if err != nil {
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
