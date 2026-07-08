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
	Content   string `json:"content"`   // 正常回答文本片段
	Reasoning string `json:"reasoning"` // 推理/思考文本片段 (DeepSeek-R1 等)
	Done      bool   `json:"done"`      // 是否为结束标记
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
func (c *AIClient) ChatCompletionStream(ctx context.Context, messages []ChatMessage, maxOutputTokens int, onChunk func(StreamChunk) error) error {
	// 上下文压缩：如果超过最大字符限制，自动压缩 messages
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

		var delta streamDelta
		if err := json.Unmarshal([]byte(data), &delta); err != nil {
			logger.WithComponent("ai").Warn("failed to parse stream delta", zap.Error(err), zap.String("data", data))
			continue
		}

		if len(delta.Choices) > 0 {
			d := delta.Choices[0].Delta
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
	// 上下文压缩：如果超过最大字符限制，自动压缩 messages
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

// compressMessages 当上下文超过限制时，压缩 messages：
// 1. 始终保留 system message
// 2. 保留最近的对话历史
// 3. 截断最早的 user/assistant 消息
func (c *AIClient) compressMessages(messages []ChatMessage) []ChatMessage {
	totalChars := c.countContextChars(messages)
	if totalChars <= c.maxContextChars {
		return messages // 不需要压缩
	}

	logger.WithComponent("ai").Warn("context exceeds max length, compressing",
		zap.Int("max_context_chars", c.maxContextChars),
		zap.Int("current_chars", totalChars),
		zap.Int("message_count", len(messages)))

	// 策略：保留 system + 最近消息，截断最早的部分
	// 1. 找到 system message 索引
	systemIdx := -1
	for i, m := range messages {
		if m.Role == "system" {
			systemIdx = i
			break
		}
	}

	// 2. 计算剩余可用字符 (预留 10% 缓冲)
	availableChars := int(float64(c.maxContextChars) * 0.9)
	usedChars := 0

	// 3. 保留 system message (如果存在)
	var result []ChatMessage
	if systemIdx >= 0 {
		result = append(result, messages[systemIdx])
		usedChars += utf8.RuneCountInString(messages[systemIdx].Role) + utf8.RuneCountInString(messages[systemIdx].Content)
	}

	// 4. 从后往前添加消息，直到达到限制
	for i := len(messages) - 1; i >= 0; i-- {
		if i == systemIdx {
			continue // 跳过 system message (已添加)
		}
		charCount := utf8.RuneCountInString(messages[i].Role) + utf8.RuneCountInString(messages[i].Content)
		if usedChars+charCount > availableChars {
			// 截断当前消息内容以适应剩余空间
			remaining := availableChars - usedChars
			if remaining > 0 {
				truncated := c.truncateMessage(messages[i], remaining)
				result = append([]ChatMessage{truncated}, result...)
				usedChars += remaining
			}
			break
		}
		result = append([]ChatMessage{messages[i]}, result...)
		usedChars += charCount
	}

	logger.WithComponent("ai").Info("context compression done",
		zap.Int("original_count", len(messages)),
		zap.Int("compressed_count", len(result)),
		zap.Int("original_chars", totalChars),
		zap.Int("compressed_chars", c.countContextChars(result)))

	return result
}

// truncateMessage 截断消息内容以适应剩余空间
func (c *AIClient) truncateMessage(msg ChatMessage, remaining int) ChatMessage {
	if remaining <= 0 {
		return msg
	}
	// 保留 role，截断 content
	truncated := msg.Content[:remaining-2] + "...\n[上下文已截断]"
	return ChatMessage{
		Role:    msg.Role,
		Content: truncated,
	}
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

