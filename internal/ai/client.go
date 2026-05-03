package ai

import (
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

	"github.com/trendradar/backend-go/pkg/config"
	"github.com/trendradar/backend-go/pkg/logger"
	"go.uber.org/zap"
)

// AIClient AI 客户端
type AIClient struct {
	model       string
	apiKey      string
	apiBase     string
	timeout     time.Duration
	temperature float64
	maxTokens   int
	numRetries  int
	client      *http.Client
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
		model:       cfg.Model,
		apiKey:      cfg.APIKey,
		apiBase:     cfg.APIBase,
		timeout:     time.Duration(cfg.Timeout) * time.Second,
		temperature: cfg.Temperature,
		maxTokens:   cfg.MaxTokens,
		numRetries:  cfg.NumRetries,
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

