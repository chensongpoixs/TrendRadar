package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
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
	Model      string        `json:"model"`
	Messages   []ChatMessage `json:"messages"`
	Temperature float64      `json:"temperature,omitempty"`
	MaxTokens   int         `json:"max_tokens,omitempty"`
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

// Chat 调用 AI 模型进行对话
func (c *AIClient) Chat(messages []ChatMessage) (string, error) {
	// 构建请求
	req := ChatRequest{
		Model:      c.model,
		Messages:   messages,
		Temperature: c.temperature,
	}

	if c.maxTokens > 0 {
		req.MaxTokens = c.maxTokens
	}

	// 序列化请求
	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	// 确定 API 端点
	apiURL := c.getAPIURL()

	// 重试逻辑
	var lastError error
	for attempt := 0; attempt <= c.numRetries; attempt++ {
		resp, err := c.doRequest(apiURL, body)
		if err == nil {
			return resp, nil
		}
		lastError = err

		if attempt < c.numRetries {
			log.Printf("AI request failed, retrying (%d/%d)...", attempt+1, c.numRetries)
			time.Sleep(time.Duration(attempt+1) * time.Second)
		}
	}

	return "", fmt.Errorf("AI request failed after %d retries: %w", c.numRetries+1, lastError)
}

// doRequest 执行 HTTP 请求
func (c *AIClient) doRequest(apiURL string, body []byte) (string, error) {
	req, err := http.NewRequest("POST", apiURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}

	// 设置头
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	// 执行请求
	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	// 解析响应
	var chatResp ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return "", err
	}

	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}

	return chatResp.Choices[0].Message.Content, nil
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
		// OpenAI 兼容服务常见入口：{base}/v1/chat/completions
		return base + "/v1/chat/completions"
	}

	// 默认使用 OpenAI 兼容端点
	return "https://api.openai.com/v1/chat/completions"
}

// AnalyzeNews 分析新闻内容
func (c *AIClient) AnalyzeNews(newsContent string, prompt string) (AnalysisResult, error) {
	messages := []ChatMessage{
		{
			Role:    "system",
			Content: "你是一个新闻分析专家，请分析以下新闻内容。",
		},
		{
			Role:    "user",
			Content: fmt.Sprintf("%s\n\n新闻内容：\n%s", prompt, newsContent),
		},
	}

	response, err := c.Chat(messages)
	if err != nil {
		return AnalysisResult{}, err
	}

	return AnalysisResult{
		RawResponse: response,
	}, nil
}

