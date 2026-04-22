package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/trendradar/backend-go/pkg/config"
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

// NewAIClient 创建 AI 客户端
func NewAIClient() *AIClient {
	cfg := config.Get().AI

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

// Chat 调用 AI 模型进行对话（使用 background context）
func (c *AIClient) Chat(messages []ChatMessage) (string, error) {
	return c.ChatWithContext(context.Background(), messages)
}

// ChatWithContext 调用 AI 模型（可取消/超时）
func (c *AIClient) ChatWithContext(ctx context.Context, messages []ChatMessage) (string, error) {
	req := ChatRequest{
		Model:       c.model,
		Messages:    messages,
		Temperature: c.temperature,
	}
	if c.maxTokens > 0 {
		req.MaxTokens = c.maxTokens
	}

	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	apiURL := c.getAPIURL()

	var lastError error
	for attempt := 0; attempt <= c.numRetries; attempt++ {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		start := time.Now()
		content, usage, err := c.doRequest(ctx, apiURL, body)
		elapsed := time.Since(start)

		if err == nil {
			log.Printf("AI request OK (%s) tokens: prompt=%d completion=%d total=%d",
				elapsed.Round(time.Millisecond), usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens)
			return content, nil
		}

		lastError = err

		if !isRetryable(err) {
			log.Printf("AI request failed (non-retryable): %v", err)
			return "", err
		}

		if attempt < c.numRetries {
			backoff := expBackoff(attempt)
			log.Printf("AI request failed (%s), retrying (%d/%d) after %s: %v",
				elapsed.Round(time.Millisecond), attempt+1, c.numRetries, backoff, err)
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(backoff):
			}
		}
	}

	return "", fmt.Errorf("AI request failed after %d retries: %w", c.numRetries+1, lastError)
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

type usageInfo struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// doRequest 执行 HTTP 请求，返回内容、token 用量与错误
func (c *AIClient) doRequest(ctx context.Context, apiURL string, body []byte) (string, usageInfo, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(body))
	if err != nil {
		return "", usageInfo{}, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return "", usageInfo{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", usageInfo{}, &apiError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}

	var chatResp ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return "", usageInfo{}, err
	}

	if len(chatResp.Choices) == 0 {
		return "", usageInfo{}, fmt.Errorf("no choices in response")
	}

	u := usageInfo{
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

