package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/trendradar/backend-go/pkg/config"
	"github.com/trendradar/backend-go/pkg/logger"
	"go.uber.org/zap"
)

// AIClient AI 客户端
type AIClient struct {
	model           string
	apiKey          string
	apiBase         string
	timeout         time.Duration
	temperature     float64
	maxTokens       int
	numRetries      int
	maxContextChars int // 最大上下文字符数 (rune)，0=不限制
	client          *http.Client
}

// ChatMessage 聊天消息
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest 聊天请求
type ChatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Temperature float64       `json:"temperature,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
}

// ChatResponse 聊天响应
type ChatResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Message ChatMessage `json:"message"`
		Index   int         `json:"index"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// StreamChatRequest 流式聊天请求
type StreamChatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Temperature float64       `json:"temperature,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Stream      bool          `json:"stream"`
}

// StreamChunk 流式响应的单块数据
type StreamChunk struct {
	Content         string `json:"content"`         // 正常回答文本片段
	Reasoning       string `json:"reasoning"`       // 推理/思考文本片段 (DeepSeek-R1 等)
	Done            bool   `json:"done"`            // 是否为结束标记
	Usage           *UsageInfo `json:"-"`         // 可选：token 用量（通常在最后一个 SSE 事件中附带）
	MaxContextChars int    `json:"-"`             // 最大上下文字符数（用于前端显示上下文窗口占用）
	MaxTokens       int    `json:"-"`             // 最大输出 token 数（用于前端显示输出进度）
	// 时间统计（用于前端显示）
	PromptTimeMs     float64 `json:"-"` // prompt 处理耗时（ms）
	GenerationTimeMs float64 `json:"-"` // token 生成耗时（ms）
}

// streamUsage 解析 OpenAI 流式响应中的 usage 结构
type streamUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// streamUsageEvent 带 usage 的 SSE 事件
type streamUsageEvent struct {
	Usage *streamUsage `json:"usage"`
}

// streamDelta 解析 OpenAI 流式响应中的 delta 结构
type streamDelta struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"delta"`
		Index int `json:"index"`
	} `json:"choices"`
}

// ChatCompletionStream 发起流式聊天补全请求，通过 onChunk 回调逐块返回内容。
// ctx 支持客户端断开时取消；maxOutputTokens ≤0 使用全局配置。
// 上下文压缩在阈值触发（非超限后才压缩），避免超出模型限制。
// 返回完整 token 用量与时间统计。
func (c *AIClient) ChatCompletionStream(ctx context.Context, messages []ChatMessage, maxOutputTokens int, onChunk func(StreamChunk) error) error {
	// 上下文压缩：如果超过阈值，自动摘要压缩早期对话
	if c.maxContextChars > 0 {
		messages = c.compressMessages(messages)
	}

	req := StreamChatRequest{
		Model:       c.model,
		Messages:    messages,
		Temperature: c.temperature,
		Stream:      true,
	}
	switch {
	case maxOutputTokens > 0:
		req.MaxTokens = maxOutputTokens
	case c.maxTokens > 0:
		req.MaxTokens = c.maxTokens
	}

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal stream request: %w", err)
	}

	apiURL := c.getAPIURL()
	httpReq, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(body))
	if err != nil {
		return err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	logger.WithComponent("ai").Info("http stream request",
		zap.String("method", httpReq.Method),
		zap.String("url", apiURL),
		zap.Any("request_headers", httpReq.Header),
		zap.String("request_body", string(body)),
	)

	start := time.Now()
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("stream request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		logger.WithComponent("ai").Error("stream response error",
			zap.Int("status", resp.StatusCode),
			zap.String("body", string(respBody)),
		)
		return &apiError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}

	scanner := bufio.NewScanner(resp.Body)
	// 增大 buffer 以容纳较大的 SSE 行
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var finalUsage *UsageInfo
	var promptTimeMs, generationTimeMs float64
	firstTokenTime := time.Duration(0) // 首 token 时间（TTFT）

	for scanner.Scan() {
		// 检查 context 是否已取消（客户端断开）
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			if err := onChunk(StreamChunk{Done: true}); err != nil {
				return err
			}
			return nil
		}

		// 尝试解析为 delta 事件（正常流式内容）
		var delta streamDelta
		if err := json.Unmarshal([]byte(data), &delta); err == nil && len(delta.Choices) > 0 {
			d := delta.Choices[0].Delta
			// 记录首 token 时间（TTFT）
			if firstTokenTime == 0 && (d.Content != "" || d.ReasoningContent != "") {
				firstTokenTime = time.Since(start)
			}
			if d.ReasoningContent != "" {
				if err := onChunk(StreamChunk{Reasoning: d.ReasoningContent}); err != nil {
					return err
				}
			}
			if d.Content != "" {
				if err := onChunk(StreamChunk{Content: d.Content}); err != nil {
					return err
				}
			}
		}

		// 尝试解析为带 usage 的事件（OpenAI 在流最后附带 token 用量）
		// 该事件可能同时包含 choices 和 usage，因此单独解析
		var usageEvt streamUsageEvent
		if err := json.Unmarshal([]byte(data), &usageEvt); err == nil && usageEvt.Usage != nil {
			promptTimeMs = float64(firstTokenTime.Microseconds()) / 1000.0 // ms
			generationTimeMs = float64(time.Since(start).Microseconds()-firstTokenTime.Microseconds()) / 1000.0
			finalUsage = &UsageInfo{
				PromptTokens:     usageEvt.Usage.PromptTokens,
				CompletionTokens: usageEvt.Usage.CompletionTokens,
				TotalTokens:      usageEvt.Usage.TotalTokens,
			}
			if err := onChunk(StreamChunk{
				Done:             true,
				Usage:            finalUsage,
				PromptTimeMs:     promptTimeMs,
				GenerationTimeMs: generationTimeMs,
			}); err != nil {
				return err
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("stream read error: %w", err)
	}

	// 如果 scanner 正常结束但没有收到 [DONE]，也发送结束标记
	return onChunk(StreamChunk{Done: true})
}

// apiError 用于区分 HTTP 状态码的错误
type apiError struct {
	StatusCode int
	Body       string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("API returned status %d: %s", e.StatusCode, e.Body)
}

func isRetryable(err error) bool {
	if ae, ok := err.(*apiError); ok {
		return ae.StatusCode == 429 || ae.StatusCode >= 500
	}
	// 网络超时、连接重置等视为可重试
	return true
}

// NewAIClient 创建 AI 客户端（使用全局 ai 配置）
func NewAIClient() *AIClient {
	return NewAIClientFromConfig(config.Get().AI)
}

// NewAIClientFromConfig 根据给定 AIConfig 创建客户端，支持各子模块传入合并后的独立配置
func NewAIClientFromConfig(cfg config.AIConfig) *AIClient {
	return &AIClient{
		model:           cfg.Model,
		apiKey:          cfg.APIKey,
		apiBase:         cfg.APIBase,
		timeout:         time.Duration(cfg.Timeout) * time.Second,
		temperature:     cfg.Temperature,
		maxTokens:       cfg.MaxTokens,
		numRetries:      cfg.NumRetries,
		maxContextChars: cfg.MaxContextChars,
		client: &http.Client{
			Timeout: time.Duration(cfg.Timeout) * time.Second,
		},
	}
}

// UsageInfo 单次补全的 token 用量
type UsageInfo struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Chat 调用 AI 模型进行对话（使用 background context）
func (c *AIClient) Chat(messages []ChatMessage) (string, error) {
	s, _, err := c.chatWithMaxOutput(context.Background(), messages, 0)
	return s, err
}

// ChatWithMaxOutput 使用指定 max_tokens（>0 时覆盖全局 ai.max_tokens），用于兴趣过滤等需长 JSON 的场景。
func (c *AIClient) ChatWithMaxOutput(messages []ChatMessage, maxOutputTokens int) (string, error) {
	s, _, err := c.chatWithMaxOutput(context.Background(), messages, maxOutputTokens)
	return s, err
}

// ChatWithContext 调用 AI 模型（可取消/超时）
func (c *AIClient) ChatWithContext(ctx context.Context, messages []ChatMessage) (string, error) {
	s, _, err := c.chatWithMaxOutput(ctx, messages, 0)
	return s, err
}

// ChatCompletion 多轮对话补全，返回回复全文与 token 用量（供 HTTP 代理等）
func (c *AIClient) ChatCompletion(ctx context.Context, messages []ChatMessage, maxOutputTokens int) (string, UsageInfo, error) {
	return c.chatWithMaxOutput(ctx, messages, maxOutputTokens)
}

// WithHTTPTimeout 返回仅 HTTP 客户端总超时不同的副本，便于对话等长耗时请求
func (c *AIClient) WithHTTPTimeout(d time.Duration) *AIClient {
	c2 := *c
	c2.client = &http.Client{Timeout: d}
	return &c2
}

func (c *AIClient) chatWithMaxOutput(ctx context.Context, messages []ChatMessage, maxOutputTokens int) (string, UsageInfo, error) {
	// 上下文压缩：如果超过阈值，自动摘要压缩早期对话
	if c.maxContextChars > 0 {
		messages = c.compressMessages(messages)
	}

	req := ChatRequest{
		Model:       c.model,
		Messages:    messages,
		Temperature: c.temperature,
	}
	switch {
	case maxOutputTokens > 0:
		req.MaxTokens = maxOutputTokens
	case c.maxTokens > 0:
		req.MaxTokens = c.maxTokens
	}

	body, err := json.Marshal(req)
	if err != nil {
		return "", UsageInfo{}, fmt.Errorf("failed to marshal request: %w", err)
	}

	apiURL := c.getAPIURL()

	var lastError error
	for attempt := 0; attempt <= c.numRetries; attempt++ {
		if ctx.Err() != nil {
			return "", UsageInfo{}, ctx.Err()
		}

		start := time.Now()
		content, usage, err := c.doRequest(ctx, apiURL, body)
		elapsed := time.Since(start)

		if err == nil {
			u := UsageInfo{
				PromptTokens:     usage.PromptTokens,
				CompletionTokens: usage.CompletionTokens,
				TotalTokens:      usage.TotalTokens,
			}
			logger.WithComponent("ai").Info("request ok",
				zap.String("elapsed", elapsed.Round(time.Millisecond).String()),
				zap.Int("prompt_tokens", u.PromptTokens),
				zap.Int("completion_tokens", u.CompletionTokens),
				zap.Int("total_tokens", u.TotalTokens),
				zap.String("assistant_message_content_full", content),
			)
			return content, u, nil
		}

		lastError = err

		if !isRetryable(err) {
			logger.WithComponent("ai").Error("request non-retryable", zap.Error(err))
			return "", UsageInfo{}, err
		}

		if attempt < c.numRetries {
			backoff := expBackoff(attempt)
			logger.WithComponent("ai").Warn("request failed, will retry",
				zap.String("elapsed", elapsed.Round(time.Millisecond).String()),
				zap.Int("attempt", attempt+1), zap.Int("max_retries", c.numRetries),
				zap.String("backoff", backoff.String()), zap.Error(err))
			select {
			case <-ctx.Done():
				return "", UsageInfo{}, ctx.Err()
			case <-time.After(backoff):
			}
		}
	}

	return "", UsageInfo{}, fmt.Errorf("AI request failed after %d retries: %w", c.numRetries+1, lastError)
}

// expBackoff 指数退避 + 随机抖动
func expBackoff(attempt int) time.Duration {
	base := math.Pow(2, float64(attempt)) * 1000 // ms
	jitter := rand.Float64() * 500                // 0-500ms
	ms := base + jitter
	if ms > 30000 {
		ms = 30000
	}
	return time.Duration(ms) * time.Millisecond
}

type rawUsageInfo struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// doRequest 执行 HTTP 请求，返回内容、token 用量与错误
func (c *AIClient) doRequest(ctx context.Context, apiURL string, body []byte) (string, rawUsageInfo, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(body))
	if err != nil {
		return "", rawUsageInfo{}, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	fullURL := req.URL.String()
	if fullURL == "" {
		fullURL = apiURL
	}
	logger.WithComponent("ai").Info("http request full",
		zap.String("method", req.Method),
		zap.String("url", fullURL),
		zap.Any("request_headers", req.Header),
		zap.String("request_body", string(body)),
	)

	resp, err := c.client.Do(req)
	if err != nil {
		return "", rawUsageInfo{}, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", rawUsageInfo{}, err
	}
	logger.WithComponent("ai").Info("http response full",
		zap.Int("status", resp.StatusCode),
		zap.Any("response_headers", resp.Header),
		zap.String("response_body", string(respBody)),
	)

	if resp.StatusCode != http.StatusOK {
		return "", rawUsageInfo{}, &apiError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return "", rawUsageInfo{}, err
	}

	if len(chatResp.Choices) == 0 {
		return "", rawUsageInfo{}, fmt.Errorf("no choices in response")
	}

	u := rawUsageInfo{
		PromptTokens:     chatResp.Usage.PromptTokens,
		CompletionTokens: chatResp.Usage.CompletionTokens,
		TotalTokens:      chatResp.Usage.TotalTokens,
	}

	return chatResp.Choices[0].Message.Content, u, nil
}

// countContextChars 计算 messages 总字符数 (rune)
func (c *AIClient) countContextChars(messages []ChatMessage) int {
	total := 0
	for _, m := range messages {
		total += utf8.RuneCountInString(m.Role) + utf8.RuneCountInString(m.Content)
	}
	return total
}

// contextCompressThreshold 返回触发压缩的字符阈值
func (c *AIClient) contextCompressThreshold() int {
	if c.maxContextChars <= 0 {
		return 0 // 不限制，不触发
	}
	threshold := config.Get().AI.ContextCompressThreshold
	if threshold <= 0 || threshold > 1 {
		threshold = 0.7
	}
	return int(float64(c.maxContextChars) * threshold)
}

// contextKeepRounds 返回保留原文的最近对话轮数
func (c *AIClient) contextKeepRounds() int {
	rounds := config.Get().AI.ContextKeepRounds
	if rounds <= 0 {
		rounds = 6
	}
	return rounds
}

// contextSummaryMaxChars 返回摘要最大字符数
func (c *AIClient) contextSummaryMaxChars() int {
	maxChars := config.Get().AI.ContextSummaryMaxChars
	if maxChars <= 0 {
		maxChars = 2000
	}
	return maxChars
}

// getSummaryModel 返回摘要使用的模型（优先专用模型，否则使用默认模型）
func (c *AIClient) getSummaryModel() string {
	summaryModel := config.Get().AI.ContextSummaryModel
	if summaryModel != "" {
		return summaryModel
	}
	return c.model
}

// compressMessages 当上下文超过阈值时，压缩 messages：
//
// 策略（混合方案）：
// 1. 始终保留 system message
// 2. 保留最近的 K 轮对话原文（user+assistant 配对）
// 3. 更早的对话消息，尝试用 AI 摘要压缩
// 4. 摘要失败时降级为截断策略
// 5. 如果仍超限，截断最近对话
func (c *AIClient) compressMessages(messages []ChatMessage) []ChatMessage {
	totalChars := c.countContextChars(messages)
	threshold := c.contextCompressThreshold()

	// 未达到压缩阈值，不需要压缩
	if threshold <= 0 || totalChars <= threshold {
		return messages
	}

	logger.WithComponent("ai").Warn("context exceeds compression threshold, compressing",
		zap.Int("threshold", threshold),
		zap.Int("current_chars", totalChars),
		zap.Int("max_context_chars", c.maxContextChars),
		zap.Int("message_count", len(messages)))

	// 找到 system message 索引
	systemIdx := -1
	for i, m := range messages {
		if m.Role == "system" {
			systemIdx = i
			break
		}
	}

	// 提取非 system 消息及其索引
	var nonSystemMsgs []struct {
		index int
		msg   ChatMessage
	}
	for i, m := range messages {
		if i != systemIdx {
			nonSystemMsgs = append(nonSystemMsgs, struct {
				index int
				msg   ChatMessage
			}{index: i, msg: m})
		}
	}

	// 计算需要保留的最近轮数（每轮 2 条消息：user + assistant）
	keepPairs := c.contextKeepRounds()
	keepCount := keepPairs * 2
	if keepCount > len(nonSystemMsgs) {
		keepCount = len(nonSystemMsgs)
	}

	// 分离：早期消息（需要压缩）和最近消息（保留原文）
	var earlyMsgs []ChatMessage
	var recentMsgs []ChatMessage
	if len(nonSystemMsgs) > keepCount {
		earlyMsgs = make([]ChatMessage, 0, len(nonSystemMsgs)-keepCount)
		for _, m := range nonSystemMsgs[:len(nonSystemMsgs)-keepCount] {
			earlyMsgs = append(earlyMsgs, m.msg)
		}
		for _, m := range nonSystemMsgs[len(nonSystemMsgs)-keepCount:] {
			recentMsgs = append(recentMsgs, m.msg)
		}
	} else {
		// 所有消息都可以保留（但可能仍需整体截断）
		for _, m := range nonSystemMsgs {
			recentMsgs = append(recentMsgs, m.msg)
		}
	}

	var result []ChatMessage

	// 添加 system message
	if systemIdx >= 0 {
		result = append(result, messages[systemIdx])
	}

	// 尝试对早期消息进行摘要压缩
	if len(earlyMsgs) > 0 {
		summary := c.summarizeMessages(earlyMsgs)
		if summary != "" {
			result = append(result, ChatMessage{
				Role:    "system",
				Content: "[对话历史摘要]\n" + summary,
			})
			logger.WithComponent("ai").Info("message summary generated",
				zap.Int("early_messages", len(earlyMsgs)),
				zap.Int("summary_chars", utf8.RuneCountInString(summary)))
		} else {
			// 摘要失败，降级为截断（保留最早的几条作为上下文提示）
			logger.WithComponent("ai").Warn("message summary failed, falling back to truncation")
			result = append(result, ChatMessage{
				Role:    "system",
				Content: "[早期对话已被截断，仅保留最近对话]",
			})
		}
	}

	// 添加最近消息
	result = append(result, recentMsgs...)

	compressedChars := c.countContextChars(result)
	logger.WithComponent("ai").Info("context compression done",
		zap.Int("original_count", len(messages)),
		zap.Int("compressed_count", len(result)),
		zap.Int("original_chars", totalChars),
		zap.Int("compressed_chars", compressedChars),
		zap.Int("early_messages_summarized", len(earlyMsgs)),
		zap.Int("recent_messages_kept", len(recentMsgs)))

	// 最终检查：如果仍然超限，截断最近消息
	if c.maxContextChars > 0 && compressedChars > c.maxContextChars {
		logger.WithComponent("ai").Warn("still exceeds max after compression, truncating recent messages",
			zap.Int("compressed_chars", compressedChars),
			zap.Int("max_context_chars", c.maxContextChars))
		result = c.truncateRecentMessages(result)
	}

	return result
}

// truncateRecentMessages 截断最近消息以适应限制
func (c *AIClient) truncateRecentMessages(messages []ChatMessage) []ChatMessage {
	if c.maxContextChars <= 0 {
		return messages
	}

	// 找到 system message 索引
	systemIdx := -1
	for i, m := range messages {
		if m.Role == "system" {
			systemIdx = i
			break
		}
	}

	var result []ChatMessage
	usedChars := 0

	// 保留 system message
	if systemIdx >= 0 {
		usedChars += utf8.RuneCountInString(messages[systemIdx].Role) + utf8.RuneCountInString(messages[systemIdx].Content)
		result = append(result, messages[systemIdx])
	}

	// 从后往前保留消息
	for i := len(messages) - 1; i >= 0; i-- {
		if i == systemIdx {
			continue
		}
		charCount := utf8.RuneCountInString(messages[i].Role) + utf8.RuneCountInString(messages[i].Content)
		if c.maxContextChars > 0 && usedChars+charCount > c.maxContextChars {
			// 截断以适应
			remaining := c.maxContextChars - usedChars
			if remaining > 20 { // 至少留一些空间
				truncated := c.truncateMessage(messages[i], remaining)
				result = append([]ChatMessage{truncated}, result...)
			}
			break
		}
		result = append([]ChatMessage{messages[i]}, result...)
		usedChars += charCount
	}

	return result
}

// truncateMessage 截断消息内容以适应剩余空间
func (c *AIClient) truncateMessage(msg ChatMessage, remaining int) ChatMessage {
	if remaining <= 0 {
		return msg
	}
	runeCount := utf8.RuneCountInString(msg.Content)
	if runeCount <= remaining {
		return msg
	}
	// 保留 role，截断 content
	truncated := string([]rune(msg.Content)[:max(0, remaining-2)]) + "...\n[已截断]"
	return ChatMessage{
		Role:    msg.Role,
		Content: truncated,
	}
}

// summarizeMessages 使用 AI 对早期对话消息生成摘要。
// 返回格式化的摘要文本，失败或空输入时返回空字符串。
func (c *AIClient) summarizeMessages(messages []ChatMessage) string {
	if len(messages) == 0 {
		return ""
	}

	// 构建摘要请求的对话内容
	var contentBuilder strings.Builder
	for _, m := range messages {
		roleLabel := map[string]string{
			"user": "用户", "assistant": "助手", "system": "系统",
		}[m.Role]
		if roleLabel == "" {
			roleLabel = m.Role
		}
		contentBuilder.WriteString(fmt.Sprintf("[%s]: %s\n", roleLabel, m.Content))
	}

	prompt := `你是一个对话摘要助手。请总结以下对话片段，保留：
1. 每个角色（用户/助手）的核心观点和问题
2. 关键事实、数据、代码片段
3. 对话的主题和进展

要求：
- 简洁明了，去除客套话和重复内容
- 保留所有技术细节、数字、代码示例
- 用中文总结（如果对话是中文）
- 每轮对话用一段话概括

对话片段：
` + contentBuilder.String()

	// 创建摘要请求
	summaryMessages := []ChatMessage{
		{Role: "user", Content: prompt},
	}

	// 使用专用摘要客户端（可能使用更便宜/更快的模型）
	summaryClient := c.withSummaryModel()

	// 设置较短的超时和 max_tokens
	origTimeout := c.timeout
	origMaxTokens := c.maxTokens
	c.timeout = 30 * time.Second
	c.maxTokens = c.contextSummaryMaxChars() / 4 // 摘要不需要太多输出

	// 执行摘要请求
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	reply, _, err := summaryClient.chatWithMaxOutput(ctx, summaryMessages, c.contextSummaryMaxChars()/4)
	// 恢复原始配置
	c.timeout = origTimeout
	c.maxTokens = origMaxTokens

	if err != nil {
		logger.WithComponent("ai").Warn("context summary failed", zap.Error(err))
		return ""
	}

	// 清理摘要：去除可能的标记
	summary := strings.TrimSpace(reply)
	// 去除可能的 "以下是摘要：" 等前缀
	summary = strings.ReplaceAll(summary, "以下是摘要：", "")
	summary = strings.ReplaceAll(summary, "以下是摘要:", "")
	summary = strings.ReplaceAll(summary, "摘要如下：", "")
	summary = strings.ReplaceAll(summary, "摘要如下:", "")

	// 限制摘要长度
	if utf8.RuneCountInString(summary) > c.contextSummaryMaxChars() {
		summary = string([]rune(summary)[:c.contextSummaryMaxChars()]) + "..."
	}

	return summary
}

// withSummaryModel 返回一个使用摘要专用模型的客户端副本
func (c *AIClient) withSummaryModel() *AIClient {
	model := c.getSummaryModel()
	if model == c.model {
		return c
	}
	c2 := *c
	c2.model = model
	return &c2
}

// getAPIURL 获取 API URL
func (c *AIClient) getAPIURL() string {
	if c.apiBase != "" {
		base := strings.TrimRight(c.apiBase, "/")
		if strings.HasSuffix(base, "/chat/completions") {
			return base
		}
		if strings.HasSuffix(base, "/v1") {
			return base + "/chat/completions"
		}
		return base + "/v1/chat/completions"
	}
	return "https://api.openai.com/v1/chat/completions"
}

