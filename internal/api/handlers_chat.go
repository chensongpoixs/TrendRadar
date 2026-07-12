package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/trendradar/backend-go/internal/ai"
	"github.com/trendradar/backend-go/pkg/config"
	applog "github.com/trendradar/backend-go/pkg/logger"
	"go.uber.org/zap"
)

const (
	chatMaxMessages     = 48
	chatMaxMessageRunes = 16000
	chatMaxTotalRunes   = 200000
	chatMaxOutputCap    = 16000
	chatHTTPTimeout     = 5 * time.Minute
)

// PostAIChat 将多轮消息转发到后端配置的 LLM，API Key 不暴露给浏览器
func PostAIChat(c *gin.Context) {
	cfg := config.Get()
	if cfg == nil || strings.TrimSpace(cfg.AI.APIKey) == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false,
			"error":   "未配置 AI API Key，无法使用对话",
		})
		return
	}

	var body struct {
		Messages []struct {
			Role    string `json:"role" binding:"required"`
			Content string `json:"content" binding:"required"`
		} `json:"messages" binding:"required"`
		MaxTokens int `json:"max_tokens"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}
	if n := len(body.Messages); n == 0 || n > chatMaxMessages {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "messages 条数须在 1～48 之间",
		})
		return
	}

	msgs := make([]ai.ChatMessage, 0, len(body.Messages))
	totalRunes := 0
	for _, m := range body.Messages {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if role != "system" && role != "user" && role != "assistant" {
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false,
				"error":   "仅支持 role 为 system、user、assistant",
			})
			return
		}
		content := strings.TrimSpace(m.Content)
		if content == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false,
				"error":   "消息 content 不能为空",
			})
			return
		}
		rn := utf8.RuneCountInString(content)
		if rn > chatMaxMessageRunes {
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false,
				"error":   "单条消息过长",
			})
			return
		}
		totalRunes += rn
		if totalRunes > chatMaxTotalRunes {
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false,
				"error":   "消息总长度超出限制",
			})
			return
		}
		msgs = append(msgs, ai.ChatMessage{Role: role, Content: content})
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), chatHTTPTimeout)
	defer cancel()

	maxTok := body.MaxTokens
	if maxTok < 0 {
		maxTok = 0
	}
	if maxTok > chatMaxOutputCap {
		maxTok = chatMaxOutputCap
	}

	client := ai.NewAIClient().WithHTTPTimeout(chatHTTPTimeout)
	reply, usage, err := client.ChatCompletion(ctx, msgs, maxTok)
	if err != nil {
		applog.WithComponent("api").Warn("ai chat", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"message": ai.ChatMessage{
				Role:    "assistant",
				Content: reply,
			},
			"usage":   usage,
			"model":   cfg.AI.Model,
			"timeout": chatHTTPTimeout.String(),
		},
	})
}

// PostAIChatStream 流式 SSE 端点：将多轮消息转发到后端 LLM，逐 token 推送给前端。
// 采用业界标准 SSE 协议 (text/event-stream + data: {...}\n\n)。
func PostAIChatStream(c *gin.Context) {
	cfg := config.Get()
	if cfg == nil || strings.TrimSpace(cfg.AI.APIKey) == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false,
			"error":   "未配置 AI API Key，无法使用对话",
		})
		return
	}

	var body struct {
		Messages []struct {
			Role    string `json:"role" binding:"required"`
			Content string `json:"content" binding:"required"`
		} `json:"messages" binding:"required"`
		MaxTokens int `json:"max_tokens"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}
	if n := len(body.Messages); n == 0 || n > chatMaxMessages {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "messages 条数须在 1～48 之间",
		})
		return
	}

	msgs := make([]ai.ChatMessage, 0, len(body.Messages))
	totalRunes := 0
	for _, m := range body.Messages {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if role != "system" && role != "user" && role != "assistant" {
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false,
				"error":   "仅支持 role 为 system、user、assistant",
			})
			return
		}
		content := strings.TrimSpace(m.Content)
		if content == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false,
				"error":   "消息 content 不能为空",
			})
			return
		}
		rn := utf8.RuneCountInString(content)
		if rn > chatMaxMessageRunes {
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false,
				"error":   "单条消息过长",
			})
			return
		}
		totalRunes += rn
		if totalRunes > chatMaxTotalRunes {
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false,
				"error":   "消息总长度超出限制",
			})
			return
		}
		msgs = append(msgs, ai.ChatMessage{Role: role, Content: content})
	}

	maxTok := body.MaxTokens
	if maxTok < 0 {
		maxTok = 0
	}
	if maxTok > chatMaxOutputCap {
		maxTok = chatMaxOutputCap
	}

	// 设置 SSE 响应头
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no") // 禁用 nginx 缓冲
	c.Status(http.StatusOK)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "streaming not supported",
		})
		return
	}

	// 发送初始连接确认事件（附带上下文窗口、max_tokens 和压缩阈值）
	writeSSE(c.Writer, flusher, map[string]interface{}{
		"type":                   "connected",
		"max_context":            cfg.AI.MaxContextChars,
		"max_tokens":             maxTok,
		"context_compress_ratio": cfg.AI.ContextCompressThreshold,
		"context_keep_rounds":    cfg.AI.ContextKeepRounds,
	})

	ctx := c.Request.Context()
	client := ai.NewAIClient().WithHTTPTimeout(chatHTTPTimeout)

	// 收集完整回复（分别追踪思考内容和正常回答）
	var fullContent strings.Builder
	var fullReasoning strings.Builder
	var finalUsage *ai.UsageInfo
	var finalTiming struct {
		PromptTimeMs     float64 `json:"prompt_time_ms"`
		GenerationTimeMs float64 `json:"generation_time_ms"`
		TotalTimeMs      float64 `json:"total_time_ms"`
	}

	err := client.ChatCompletionStream(ctx, msgs, maxTok, func(chunk ai.StreamChunk) error {
		if chunk.Done && chunk.Usage != nil {
			// 这是 OpenAI 流中附带的 token 用量事件，同时携带时间统计
			finalUsage = chunk.Usage
			finalTiming.PromptTimeMs = chunk.PromptTimeMs
			finalTiming.GenerationTimeMs = chunk.GenerationTimeMs
			finalTiming.TotalTimeMs = chunk.PromptTimeMs + chunk.GenerationTimeMs
			return nil
		}

		// 正常结束或内容块
		if chunk.Done {
			// 兜底：如果没收到 usage 事件，返回一个空 usage 对象，
			// 让前端知道"已请求过但 API 未返回 token 统计"
			if finalUsage == nil {
				finalUsage = &ai.UsageInfo{}
			}
			event := map[string]interface{}{
				"type":      "done",
				"content":   fullContent.String(),
				"reasoning": fullReasoning.String(),
				"model":     cfg.AI.Model,
				"usage":     finalUsage,
				"timing":    finalTiming,
			}
			// 如果有压缩统计信息，附加到事件中
			if chunk.MaxContextChars > 0 || chunk.MaxTokens > 0 {
				event["compression"] = map[string]interface{}{
					"max_context": chunk.MaxContextChars,
					"max_tokens":  chunk.MaxTokens,
				}
			}
			writeSSE(c.Writer, flusher, event)
			return nil
		}
		if chunk.Reasoning != "" {
			fullReasoning.WriteString(chunk.Reasoning)
		}
		if chunk.Content != "" {
			fullContent.WriteString(chunk.Content)
		}
		writeSSE(c.Writer, flusher, map[string]interface{}{
			"type":      "chunk",
			"content":   chunk.Content,
			"reasoning": chunk.Reasoning,
		})
		return nil
	})

	if err != nil {
		applog.WithComponent("api").Warn("ai chat stream", zap.Error(err))
		writeSSE(c.Writer, flusher, map[string]interface{}{
			"type":  "error",
			"error": err.Error(),
		})
	}

	fmt.Fprintf(c.Writer, "data: [DONE]\n\n")
	flusher.Flush()
}

// writeSSE 写一条 SSE 事件到响应流
func writeSSE(w http.ResponseWriter, flusher http.Flusher, data interface{}) {
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", string(b))
	flusher.Flush()
}
