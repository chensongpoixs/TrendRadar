package ai

import (
	"fmt"
	"strings"

	"github.com/trendradar/backend-go/pkg/config"
)

// Translator AI 翻译器
type Translator struct {
	client        *AIClient
	targetLang    string // 目标语言
	sourceLang    string // 源语言
	batchSize     int    // 批处理大小
	includeOrigin bool   // 是否保留原文
}

// TranslationResult 翻译结果
type TranslationResult struct {
	Original  string `json:"original"`  // 原文
	Translated string `json:"translated"` // 译文
	Confidence float64 `json:"confidence,omitempty"` // 置信度
}

// NewTranslator 创建翻译器
func NewTranslator(targetLang string, sourceLang string, batchSize int, includeOrigin bool) *Translator {
	if targetLang == "" {
		targetLang = "Simplified Chinese"
	}
	if batchSize <= 0 {
		batchSize = 20
	}

	cfg := config.Get()
	return &Translator{
		client:        NewAIClientFromConfig(cfg.AITranslation.EffectiveAIConfig(cfg.AI)),
		targetLang:    targetLang,
		sourceLang:    sourceLang,
		batchSize:     batchSize,
		includeOrigin: includeOrigin,
	}
}

// Translate 翻译文本
func (t *Translator) Translate(text string) (string, error) {
	sourceLang := t.sourceLang
	if sourceLang == "" {
		sourceLang = "auto-detect"
	}

	prompt := fmt.Sprintf("请将以下文本从 %s 翻译成 %s。\n\n只返回翻译结果，不要包含任何解释或其他内容。\n\n文本：\n%s",
		sourceLang, t.targetLang, text)

	messages := []ChatMessage{
		{
			Role:    "system",
			Content: "你是一个专业的翻译器，提供准确、流畅的翻译。",
		},
		{
			Role:    "user",
			Content: prompt,
		},
	}

	return t.client.Chat(messages)
}

// TranslateBatch 批量翻译
func (t *Translator) TranslateBatch(texts []string) ([]TranslationResult, error) {
	if len(texts) == 0 {
		return []TranslationResult{}, nil
	}

	results := make([]TranslationResult, 0, len(texts))

	// 分批处理
	for i := 0; i < len(texts); i += t.batchSize {
		end := i + t.batchSize
		if end > len(texts) {
			end = len(texts)
		}

		batch := texts[i:end]
		batchResults, err := t.translateBatch(batch)
		if err != nil {
			return nil, fmt.Errorf("failed to translate batch: %w", err)
		}

		results = append(results, batchResults...)
	}

	return results, nil
}

// translateBatch 翻译一批文本
func (t *Translator) translateBatch(texts []string) ([]TranslationResult, error) {
	sourceLang := t.sourceLang
	if sourceLang == "" {
		sourceLang = "auto-detect"
	}

	var prompt strings.Builder
	prompt.WriteString(fmt.Sprintf("请将以下文本从 %s 翻译成 %s。\n\n", sourceLang, t.targetLang))
	prompt.WriteString("请按以下 JSON 格式返回结果：\n")
	prompt.WriteString("[\n")
	prompt.WriteString("  {\n")
	prompt.WriteString("    \"index\": 0, // 原文索引（从 0 开始）\n")
	prompt.WriteString("    \"original\": \"原文\",\n")
	prompt.WriteString("    \"translated\": \"译文\"\n")
	prompt.WriteString("  }\n")
	prompt.WriteString("]\n\n")
	prompt.WriteString("文本列表：\n")

	for i, text := range texts {
		prompt.WriteString(fmt.Sprintf("%d. %s\n", i+1, text))
	}

	messages := []ChatMessage{
		{
			Role:    "system",
			Content: "你是一个专业的翻译器。请严格只返回纯 JSON 数组，不要包含 markdown 标记、代码围栏或任何解释文字。",
		},
		{
			Role:    "user",
			Content: prompt.String(),
		},
	}

	response, err := t.client.Chat(messages)
	if err != nil {
		return nil, fmt.Errorf("AI chat failed: %w", err)
	}

	type transRaw struct {
		Index      int    `json:"index"`
		Original   string `json:"original"`
		Translated string `json:"translated"`
	}
	rawResults, err := unmarshalFromLLM[[]transRaw](response)
	if err != nil {
		return nil, fmt.Errorf("failed to parse translation results: %w", err)
	}

	// 转换为 TranslationResult
	results := make([]TranslationResult, 0, len(rawResults))
	for _, raw := range rawResults {
		if raw.Index >= 0 && raw.Index < len(texts) {
			results = append(results, TranslationResult{
				Original:   raw.Original,
				Translated: raw.Translated,
			})
		}
	}

	return results, nil
}

// TranslateNews 翻译新闻标题
func (t *Translator) TranslateNews(newsItems []NewsItem) ([]NewsItem, error) {
	if len(newsItems) == 0 {
		return newsItems, nil
	}

	// 提取所有标题
	titles := make([]string, len(newsItems))
	for i, item := range newsItems {
		titles[i] = item.Title
	}

	// 批量翻译
	translations, err := t.TranslateBatch(titles)
	if err != nil {
		return nil, err
	}

	// 更新新闻项
	result := make([]NewsItem, len(newsItems))
	for i, item := range newsItems {
		result[i] = item
		if i < len(translations) {
			if t.includeOrigin {
				result[i].Title = fmt.Sprintf("%s（%s）", translations[i].Translated, item.Title)
			} else {
				result[i].Title = translations[i].Translated
			}
		}
	}

	return result, nil
}

// TranslateRSS 翻译 RSS 标题
func (t *Translator) TranslateRSS(rssItems []RSSItem) ([]RSSItem, error) {
	if len(rssItems) == 0 {
		return rssItems, nil
	}

	// 提取所有标题
	titles := make([]string, len(rssItems))
	for i, item := range rssItems {
		titles[i] = item.Title
	}

	// 批量翻译
	translations, err := t.TranslateBatch(titles)
	if err != nil {
		return nil, err
	}

	// 更新 RSS 项
	result := make([]RSSItem, len(rssItems))
	for i, item := range rssItems {
		result[i] = item
		if i < len(translations) {
			if t.includeOrigin {
				result[i].Title = fmt.Sprintf("%s（%s）", translations[i].Translated, item.Title)
			} else {
				result[i].Title = translations[i].Translated
			}
		}
	}

	return result, nil
}
