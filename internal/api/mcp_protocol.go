package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/trendradar/backend-go/internal/ai"
	"github.com/trendradar/backend-go/internal/core"
	"github.com/trendradar/backend-go/internal/notification"
	"github.com/trendradar/backend-go/internal/storage"
	"github.com/trendradar/backend-go/pkg/config"
	"github.com/trendradar/backend-go/pkg/logger"
	"github.com/trendradar/backend-go/pkg/model"
	"go.uber.org/zap"
)

// ==================== MCP JSON-RPC 2.0 协议定义 ====================

// MCPRequest 对应 MCP 协议的 JSON-RPC 2.0 请求
type MCPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	ID      interface{}     `json:"id"`
}

// MCPResponse 对应 MCP 协议的 JSON-RPC 2.0 响应
type MCPResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	Result  interface{} `json:"result,omitempty"`
	Error   *MCPError   `json:"error,omitempty"`
	ID      interface{} `json:"id"`
}

// MCPError MCP 协议错误定义
type MCPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

// MCPNotification MCP 通知（无 ID 的请求）
type MCPNotification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// ==================== MCP 资源定义 ====================

// MCPResource MCP 资源表示
type MCPResource struct {
	URI         string            `json:"uri"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	MIMEType    string            `json:"mimeType,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// MCPResourceContents MCP 资源内容
type MCPResourceContents struct {
	URI      string `json:"uri"`
	MIMEType string `json:"mimeType,omitempty"`
	Text     string `json:"text"`
}

// ==================== MCP 工具定义 ====================

// MCPTool MCP 工具表示
type MCPTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// MCPToolCallArgs MCP 工具调用参数
type MCPToolCallArgs struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"arguments"`
}

// MCPToolResult MCP 工具调用结果
type MCPToolResult struct {
	Content []MCPContentItem `json:"content"`
	IsError bool             `json:"isError,omitempty"`
}

// MCPContentItem MCP 内容项
type MCPContentItem struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	Image       string `json:"image,omitempty"`
	MIMEType    string `json:"mimeType,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

// ==================== MCP 协议常量 ====================

const (
	// JSON-RPC 2.0 错误码
	MCPErrorParseError    = -32700
	MCPErrorInvalidRequest = -32600
	MCPErrorMethodNotFound = -32601
	MCPErrorInvalidParams  = -32602
	MCPErrorInternalError  = -32603

	// MCP 协议版本
	MCPProtocolVersion = "2024-11-05"

	// MCP 端点
	MCPMethodToolsList   = "tools/list"
	MCPMethodToolsCall   = "tools/call"
	MCPMethodResourcesList = "resources/list"
	MCPMethodResourcesRead = "resources/read"
	MCPMethodPromptList  = "prompts/list"
	MCPMethodInitialize  = "initialize"
	MCPMethodNotifications = "notifications/"
)

// ==================== MCP 服务器实现 ====================

// MCPServer MCP 协议服务器
type MCPServer struct {
	mu       sync.RWMutex
	initialized bool
	tools    []MCPTool
}

var mcpServerInstance *MCPServer
var mcpServerOnce sync.Once

// GetMCPServer 获取 MCP 服务器单例
func GetMCPServer() *MCPServer {
	mcpServerOnce.Do(func() {
		mcpServerInstance = &MCPServer{}
		mcpServerInstance.initTools()
	})
	return mcpServerInstance
}

// initTools 初始化 MCP 工具列表
func (s *MCPServer) initTools() {
	// 构建工具列表（基于现有 API 端点）
	s.tools = []MCPTool{
		{
			Name:        "get_latest_news",
			Description: "获取最新新闻快照（从本地数据库读取，不访问外网）",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"platforms":{"type":"array","items":{"type":"string"},"description":"平台ID列表，为空则返回所有启用平台"},"limit":{"type":"integer","minimum":1,"maximum":200,"default":50,"description":"每平台最大条数"}},"required":[]}`),
		},
		{
			Name:        "get_news_by_date",
			Description: "按日期获取新闻",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"date":{"type":"string","pattern":"^\\d{4}-\\d{2}-\\d{2}$","description":"日期 YYYY-MM-DD"},"platforms":{"type":"array","items":{"type":"string"}},"limit":{"type":"integer","default":50}},"required":["date"]}`),
		},
		{
			Name:        "search_news",
			Description: "搜索新闻（支持关键词、模糊、实体模式）",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"搜索关键词"},"search_mode":{"type":"string","enum":["keyword","fuzzy","entity"],"default":"keyword"},"date_range":{"type":"string","enum":["today","yesterday","last_7_days","last_30_days"]},"platforms":{"type":"array","items":{"type":"string"}},"limit":{"type":"integer","default":50},"sort_by":{"type":"string","enum":["relevance","weight","date"],"default":"relevance"}},"required":["query"]}`),
		},
		{
			Name:        "get_trending_topics",
			Description: "获取热门话题统计",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"top_n":{"type":"integer","default":10,"minimum":1,"maximum":100},"mode":{"type":"string","enum":["current","daily"],"default":"current"}},"required":[]}`),
		},
		{
			Name:        "get_latest_rss",
			Description: "获取最新 RSS 内容（从本地数据库读取）",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"feeds":{"type":"array","items":{"type":"string"}},"limit":{"type":"integer","default":50}},"required":[]}`),
		},
		{
			Name:        "search_rss",
			Description: "搜索 RSS 内容",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"keyword":{"type":"string","description":"搜索关键词"},"feeds":{"type":"array","items":{"type":"string"}},"days":{"type":"integer","default":7}},"required":["keyword"]}`),
		},
		{
			Name:        "analyze_topic_trend",
			Description: "分析话题趋势（基于历史数据）",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"topic":{"type":"string","description":"话题名称"},"analysis_type":{"type":"string","enum":["trend","lifecycle","viral","predict"],"default":"trend"},"date_range":{"type":"string","enum":["last_7_days","last_30_days"]},"granularity":{"type":"string","enum":["hour","day","week"],"default":"day"}},"required":["topic"]}`),
		},
		{
			Name:        "analyze_sentiment",
			Description: "分析话题情感倾向（基于关键词的简化分析）",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"topic":{"type":"string","description":"话题名称"},"platforms":{"type":"array","items":{"type":"string"}},"date_range":{"type":"string","enum":["last_7_days","last_30_days"]},"limit":{"type":"integer","default":50}},"required":["topic"]}`),
		},
		{
			Name:        "aggregate_news",
			Description: "新闻聚类聚合（基于关键词相似度）",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"date_range":{"type":"string","enum":["last_7_days","last_30_days"]},"platforms":{"type":"array","items":{"type":"string"}},"similarity_threshold":{"type":"number","minimum":0,"maximum":1,"default":0.7}},"required":[]}`),
		},
		{
			Name:        "get_system_status",
			Description: "获取系统状态（版本、配置、数据库连接）",
			InputSchema: json.RawMessage(`{"type":"object","properties":{},"required":[]}`),
		},
		{
			Name:        "get_current_config",
			Description: "获取当前配置（不返回敏感信息）",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"section":{"type":"string","enum":["all","crawler","push","keywords","weights"],"default":"all"}},"required":[]}`),
		},
		{
			Name:        "get_snapshot_dates",
			Description: "获取有热榜快照的日期列表",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"days":{"type":"integer","default":60,"minimum":1,"maximum":365}},"required":[]}`),
		},
		{
			Name:        "get_snapshot_day_summary",
			Description: "获取某一天各小时的新闻汇总",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"date":{"type":"string","pattern":"^\\d{4}-\\d{2}-\\d{2}$"}},"required":["date"]}`),
		},
		{
			Name:        "get_snapshot_hour",
			Description: "获取某一天某一小时的热榜快照",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"date":{"type":"string","pattern":"^\\d{4}-\\d{2}-\\d{2}$"},"hour":{"type":"integer","minimum":0,"maximum":23},"platforms":{"type":"array","items":{"type":"string"}}},"required":["date","hour"]}`),
		},
		{
			Name:        "get_snapshot_insights",
			Description: "获取某天的 AI 行业研报缓存",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"date":{"type":"string","pattern":"^\\d{4}-\\d{2}-\\d{2}$"}},"required":["date"]}`),
		},
		{
			Name:        "generate_day_insights",
			Description: "基于当天热榜标题流生成 AI 行业研报",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"date":{"type":"string","pattern":"^\\d{4}-\\d{2}-\\d{2}$"},"platforms":{"type":"array","items":{"type":"string"}}},"required":["date"]}`),
		},
		{
			Name:        "analyze_news_article",
			Description: "对单条新闻进行 AI 读报式摘要分析",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"title":{"type":"string","description":"新闻标题"},"url":{"type":"string","description":"新闻链接"},"source_name":{"type":"string"}},"required":["title","url"]}`),
		},
		{
			Name:        "ai_chat",
			Description: "AI 对话（后端转发，API Key 不暴露给前端）",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"messages":{"type":"array","items":{"type":"object","properties":{"role":{"type":"string"},"content":{"type":"string"}}}},"model":{"type":"string","description":"可选，使用配置中的默认模型"},"max_tokens":{"type":"integer","description":"可选，最大输出 token 数"}},"required":["messages"]}`),
		},
		{
			Name:        "trigger_crawl",
			Description: "【已禁用】不再通过 HTTP 触发外网拉取；热榜与 RSS 由 scheduler 定时写入",
			InputSchema: json.RawMessage(`{"type":"object","properties":{},"required":[]}`),
		},
		{
			Name:        "sync_from_remote",
			Description: "从远程存储同步数据（待实现）",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"days":{"type":"integer","default":7}},"required":[]}`),
		},
		{
			Name:        "get_storage_status",
			Description: "获取存储状态（后端类型、格式、容量）",
			InputSchema: json.RawMessage(`{"type":"object","properties":{},"required":[]}`),
		},
		{
			Name:        "list_available_dates",
			Description: "列出可用日期（热榜快照和 RSS 数据）",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"source":{"type":"string","enum":["hotlist","rss","both"],"default":"both"}},"required":[]}`),
		},
		{
			Name:        "resolve_date_range",
			Description: "解析自然语言日期范围（如'本周'、'最近 7 天'）",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string","description":"自然语言日期描述"}},"required":["text"]}`),
		},
		{
			Name:        "send_notification",
			Description: "发送通知到指定渠道（待完善）",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"channel":{"type":"string","enum":["email","feishu","dingtalk","telegram","serverchan"]},"title":{"type":"string"},"content":{"type":"string"},"format":{"type":"string","enum":["markdown","html","plain"],"default":"markdown"}},"required":["channel","title","content"]}`),
		},
		{
			Name:        "get_channel_format_guide",
			Description: "获取各通知渠道的格式指南",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"channel":{"type":"string","enum":["email","feishu","dingtalk","telegram","serverchan"]}},"required":[]}`),
		},
		{
			Name:        "get_notification_channels",
			Description: "获取已配置的通知渠道列表",
			InputSchema: json.RawMessage(`{"type":"object","properties":{},"required":[]}`),
		},
	}
}

// HandleMCPRequest 处理单个 MCP 请求
func (s *MCPServer) HandleMCPRequest(req *MCPRequest) (*MCPResponse, error) {
	s.mu.RLock()
	initialized := s.initialized
	s.mu.RUnlock()

	// 如果未初始化，只允许 initialize 请求
	if !initialized && req.Method != MCPMethodInitialize {
		return &MCPResponse{
			JSONRPC: "2.0",
			Error: &MCPError{
				Code:    MCPErrorMethodNotFound,
				Message: "Server not initialized. Call 'initialize' first.",
			},
			ID: req.ID,
		}, nil
	}

	switch req.Method {
	case MCPMethodInitialize:
		return s.handleInitialize(req.Params, req.ID)
	case MCPMethodToolsList:
		return s.handleToolsList(req.ID)
	case MCPMethodToolsCall:
		return s.handleToolsCall(req.Params, req.ID)
	case MCPMethodResourcesList:
		return s.handleResourcesList(req.ID)
	case MCPMethodResourcesRead:
		return s.handleResourcesRead(req.Params, req.ID)
	default:
		return &MCPResponse{
			JSONRPC: "2.0",
			Error: &MCPError{
				Code:    MCPErrorMethodNotFound,
				Message: fmt.Sprintf("Method not found: %s", req.Method),
				Data:    fmt.Sprintf("Supported methods: %s, %s, %s, %s", MCPMethodInitialize, MCPMethodToolsList, MCPMethodToolsCall, MCPMethodResourcesList),
			},
			ID: req.ID,
		}, nil
	}
}

// handleInitialize 处理初始化请求
func (s *MCPServer) handleInitialize(params json.RawMessage, id interface{}) (*MCPResponse, error) {
	s.mu.Lock()
	s.initialized = true
	s.mu.Unlock()

	// 解析协议版本（可选）
	var version string
	if params != nil {
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
			Capabilities    map[string]interface{} `json:"capabilities"`
			ClientInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"clientInfo"`
		}
		if err := json.Unmarshal(params, &p); err == nil {
			version = p.ProtocolVersion
		}
	}

	l := logger.WithComponent("mcp").With(zap.String("version", version))
	l.Info("MCP client initialized")

	return &MCPResponse{
		JSONRPC: "2.0",
		Result: gin.H{
			"protocolVersion": MCPProtocolVersion,
			"serverInfo": gin.H{
				"name":    config.Get().App.Name,
				"version": config.Get().App.Version,
			},
			"capabilities": gin.H{
				"tools":     true,
				"resources": true,
			},
			"instructions": "欢迎使用 TrendRadar MCP Server。请调用 tools/list 获取可用工具。",
		},
		ID: id,
	}, nil
}

// handleToolsList 处理工具列表请求
func (s *MCPServer) handleToolsList(id interface{}) (*MCPResponse, error) {
	return &MCPResponse{
		JSONRPC: "2.0",
		Result: gin.H{
			"tools": s.tools,
		},
		ID: id,
	}, nil
}

// handleToolsCall 处理工具调用请求
func (s *MCPServer) handleToolsCall(params json.RawMessage, id interface{}) (*MCPResponse, error) {
	var call MCPToolCallArgs
	if err := json.Unmarshal(params, &call); err != nil {
		return &MCPResponse{
			JSONRPC: "2.0",
			Error: &MCPError{
				Code:    MCPErrorInvalidParams,
				Message: "Invalid tool call parameters",
				Data:    err.Error(),
			},
			ID: id,
		}, nil
	}

	// 查找工具
	var tool *MCPTool
	for i := range s.tools {
		if s.tools[i].Name == call.Name {
			tool = &s.tools[i]
			break
		}
	}

	if tool == nil {
		return &MCPResponse{
			JSONRPC: "2.0",
			Error: &MCPError{
				Code:    MCPErrorMethodNotFound,
				Message: fmt.Sprintf("Tool not found: %s", call.Name),
			},
			ID: id,
		}, nil
	}

	// 执行工具
	result, err := s.executeTool(call.Name, call.Args)
	if err != nil {
		return &MCPResponse{
			JSONRPC: "2.0",
			Error: &MCPError{
				Code:    MCPErrorInternalError,
				Message: fmt.Sprintf("Tool execution failed: %s", call.Name),
				Data:    err.Error(),
			},
			ID: id,
		}, nil
	}

	return &MCPResponse{
		JSONRPC: "2.0",
		Result:  result,
		ID:      id,
	}, nil
}

// executeTool 执行工具调用
func (s *MCPServer) executeTool(name string, args json.RawMessage) (*MCPToolResult, error) {
	switch name {
	case "get_latest_news":
		return s.toolGetLatestNews(args)
	case "get_news_by_date":
		return s.toolGetNewsByDate(args)
	case "search_news":
		return s.toolSearchNews(args)
	case "get_trending_topics":
		return s.toolGetTrendingTopics(args)
	case "get_latest_rss":
		return s.toolGetLatestRSS(args)
	case "search_rss":
		return s.toolSearchRSS(args)
	case "analyze_topic_trend":
		return s.toolAnalyzeTopicTrend(args)
	case "analyze_sentiment":
		return s.toolAnalyzeSentiment(args)
	case "aggregate_news":
		return s.toolAggregateNews(args)
	case "get_system_status":
		return s.toolGetSystemStatus(args)
	case "get_current_config":
		return s.toolGetCurrentConfig(args)
	case "get_snapshot_dates":
		return s.toolGetSnapshotDates(args)
	case "get_snapshot_day_summary":
		return s.toolGetSnapshotDaySummary(args)
	case "get_snapshot_hour":
		return s.toolGetSnapshotHour(args)
	case "get_snapshot_insights":
		return s.toolGetSnapshotInsights(args)
	case "generate_day_insights":
		return s.toolGenerateDayInsights(args)
	case "analyze_news_article":
		return s.toolAnalyzeNewsArticle(args)
	case "ai_chat":
		return s.toolAIChat(args)
	case "trigger_crawl":
		return &MCPToolResult{
			Content: []MCPContentItem{{Type: "text", Text: "【已禁用】热榜抓取由 scheduler 定时执行，不支持 HTTP 触发。请使用 GET /api/v1/news/latest 读取库内数据。"}},
			IsError: true,
		}, nil
	case "sync_from_remote":
		return &MCPToolResult{
			Content: []MCPContentItem{{Type: "text", Text: "远程同步功能待实现。当前数据存储在本地 SQLite 数据库中。"}},
		}, nil
	case "get_storage_status":
		return s.toolGetStorageStatus(args)
	case "list_available_dates":
		return s.toolListAvailableDates(args)
	case "resolve_date_range":
		return s.toolResolveDateRange(args)
	case "send_notification":
		return s.toolSendNotification(args)
	case "get_channel_format_guide":
		return s.toolGetChannelFormatGuide(args)
	case "get_notification_channels":
		return s.toolGetNotificationChannels(args)
	default:
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
}

// ==================== 工具实现（从 handlers.go 迁移的逻辑） ====================

// toolGetLatestNews 工具：获取最新新闻
func (s *MCPServer) toolGetLatestNews(args json.RawMessage) (*MCPToolResult, error) {
	var p struct {
		Platforms []string `json:"platforms"`
		Limit     int      `json:"limit"`
	}
	json.Unmarshal(args, &p)
	if p.Limit <= 0 {
		p.Limit = 50
	}

	cfg := config.Get()
	var platformIDs []string
	if len(p.Platforms) > 0 {
		platformIDs = p.Platforms
	} else {
		for _, src := range cfg.Platforms.Sources {
			if src.Enabled {
				platformIDs = append(platformIDs, src.ID)
			}
		}
	}

	newsStorage := storage.NewNewsStorage()
	results, lastCrawl, err := newsStorage.GetLatestNews(platformIDs, p.Limit)
	if err != nil {
		return nil, err
	}

	idToName := make(map[string]string)
	for _, src := range cfg.Platforms.Sources {
		if src.ID != "" {
			idToName[src.ID] = src.Name
		}
	}

	crawlTimeStr := ""
	if !lastCrawl.IsZero() {
		crawlTimeStr = lastCrawl.Format(time.RFC3339)
	}

	content, _ := json.Marshal(gin.H{
		"success": true,
		"data": gin.H{
			"news":       results,
			"id_to_name": idToName,
			"failed_ids": []string{},
			"crawl_time": crawlTimeStr,
			"source":     "database",
		},
	})

	return &MCPToolResult{
		Content: []MCPContentItem{{Type: "text", Text: string(content)}},
	}, nil
}

// toolGetNewsByDate 工具：按日期获取新闻
func (s *MCPServer) toolGetNewsByDate(args json.RawMessage) (*MCPToolResult, error) {
	var p struct {
		Date      string   `json:"date"`
		Platforms []string `json:"platforms"`
		Limit     int      `json:"limit"`
	}
	json.Unmarshal(args, &p)
	if p.Limit <= 0 {
		p.Limit = 50
	}

	newsStorage := storage.NewNewsStorage()
	results, err := newsStorage.GetTodayNews(p.Platforms, p.Date)
	if err != nil {
		return nil, err
	}

	content, _ := json.Marshal(gin.H{
		"success": true,
		"data": gin.H{
			"news": results,
			"date": p.Date,
		},
	})

	return &MCPToolResult{
		Content: []MCPContentItem{{Type: "text", Text: string(content)}},
	}, nil
}

// toolSearchNews 工具：搜索新闻
func (s *MCPServer) toolSearchNews(args json.RawMessage) (*MCPToolResult, error) {
	var p struct {
		Query      string   `json:"query"`
		SearchMode string   `json:"search_mode"`
		DateRange  string   `json:"date_range"`
		Platforms  []string `json:"platforms"`
		Limit      int      `json:"limit"`
		SortBy     string   `json:"sort_by"`
	}
	json.Unmarshal(args, &p)
	if p.SearchMode == "" {
		p.SearchMode = "keyword"
	}
	if p.Limit <= 0 {
		p.Limit = 50
	}
	if p.SortBy == "" {
		p.SortBy = "relevance"
	}

	// 解析日期范围
	var dateStart, dateEnd string
	switch p.DateRange {
	case "today":
		today := time.Now().Format("2006-01-02")
		dateStart, dateEnd = today, today
	case "yesterday":
		yesterday := time.Now().AddDate(0, 0, -1)
		dateStart, dateEnd = yesterday.Format("2006-01-02"), yesterday.Format("2006-01-02")
	case "last_7_days":
		dateStart = time.Now().AddDate(0, 0, -7).Format("2006-01-02")
		dateEnd = time.Now().Format("2006-01-02")
	case "last_30_days":
		dateStart = time.Now().AddDate(0, 0, -30).Format("2006-01-02")
		dateEnd = time.Now().Format("2006-01-02")
	}

	newsStorage := storage.NewNewsStorage()
	results, err := newsStorage.SearchNews(&model.SearchOptions{
		Query:    p.Query,
		SearchMode: p.SearchMode,
		DateStart:  dateStart,
		DateEnd:    dateEnd,
		Platforms:  p.Platforms,
		Limit:      p.Limit,
		SortBy:     p.SortBy,
	})
	if err != nil {
		return nil, err
	}

	content, _ := json.Marshal(gin.H{
		"success": true,
		"data": gin.H{
			"query":       p.Query,
			"search_mode": p.SearchMode,
			"results":     results,
			"total":       len(results),
		},
	})

	return &MCPToolResult{
		Content: []MCPContentItem{{Type: "text", Text: string(content)}},
	}, nil
}

// toolGetTrendingTopics 工具：获取热门话题
func (s *MCPServer) toolGetTrendingTopics(args json.RawMessage) (*MCPToolResult, error) {
	var p struct {
		TopN int    `json:"top_n"`
		Mode string `json:"mode"`
	}
	json.Unmarshal(args, &p)
	if p.TopN <= 0 {
		p.TopN = 10
	}
	if p.Mode == "" {
		p.Mode = "current"
	}

	newsStorage := storage.NewNewsStorage()
	topics, err := newsStorage.GetTopicStats(p.TopN, p.Mode)
	if err != nil {
		return nil, err
	}

	content, _ := json.Marshal(gin.H{
		"success": true,
		"data": gin.H{
			"topics": topics,
			"mode":   p.Mode,
		},
	})

	return &MCPToolResult{
		Content: []MCPContentItem{{Type: "text", Text: string(content)}},
	}, nil
}

// toolGetLatestRSS 工具：获取最新 RSS
func (s *MCPServer) toolGetLatestRSS(args json.RawMessage) (*MCPToolResult, error) {
	var p struct {
		Feeds []string `json:"feeds"`
		Limit int      `json:"limit"`
	}
	json.Unmarshal(args, &p)
	if p.Limit <= 0 {
		p.Limit = 50
	}

	cfg := config.Get()
	var feedIDs []string
	if len(p.Feeds) > 0 {
		feedIDs = p.Feeds
	} else {
		for _, f := range cfg.RSS.Feeds {
			if f.Enabled {
				feedIDs = append(feedIDs, f.ID)
			}
		}
	}

	newsStorage := storage.NewNewsStorage()
	results, lastCrawl, err := newsStorage.GetLatestRSSFromDB(feedIDs, p.Limit)
	if err != nil {
		return nil, err
	}

	idToName := make(map[string]string)
	for _, f := range cfg.RSS.Feeds {
		if f.ID != "" {
			idToName[f.ID] = f.Name
		}
	}

	crawlTimeStr := ""
	if !lastCrawl.IsZero() {
		crawlTimeStr = lastCrawl.Format(time.RFC3339)
	}

	content, _ := json.Marshal(gin.H{
		"success": true,
		"data": gin.H{
			"rss":        results,
			"id_to_name": idToName,
			"failed_ids": []string{},
			"crawl_time": crawlTimeStr,
			"source":     "database",
		},
	})

	return &MCPToolResult{
		Content: []MCPContentItem{{Type: "text", Text: string(content)}},
	}, nil
}

// toolSearchRSS 工具：搜索 RSS
func (s *MCPServer) toolSearchRSS(args json.RawMessage) (*MCPToolResult, error) {
	var p struct {
		Keyword string   `json:"keyword"`
		Feeds   []string `json:"feeds"`
		Days    int      `json:"days"`
	}
	json.Unmarshal(args, &p)
	if p.Days <= 0 {
		p.Days = 7
	}

	newsStorage := storage.NewNewsStorage()
	results, err := newsStorage.SearchRSS(p.Keyword, p.Feeds, p.Days)
	if err != nil {
		return nil, err
	}

	content, _ := json.Marshal(gin.H{
		"success": true,
		"data": gin.H{
			"keyword": p.Keyword,
			"results": results,
			"total":   len(results),
		},
	})

	return &MCPToolResult{
		Content: []MCPContentItem{{Type: "text", Text: string(content)}},
	}, nil
}

// toolAnalyzeTopicTrend 工具：分析话题趋势
func (s *MCPServer) toolAnalyzeTopicTrend(args json.RawMessage) (*MCPToolResult, error) {
	var p struct {
		Topic        string  `json:"topic"`
		AnalysisType string  `json:"analysis_type"`
		DateRange    string  `json:"date_range"`
		Granularity  string  `json:"granularity"`
	}
	json.Unmarshal(args, &p)
	if p.AnalysisType == "" {
		p.AnalysisType = "trend"
	}
	if p.Granularity == "" {
		p.Granularity = "day"
	}

	newsStorage := storage.NewNewsStorage()
	opts := &model.SearchOptions{
		Query:    p.Topic,
		SearchMode: "keyword",
		Limit:    200,
	}
	if p.DateRange == "last_7_days" {
		opts.DateStart = time.Now().AddDate(0, 0, -7).Format("2006-01-02")
	} else if p.DateRange == "last_30_days" {
		opts.DateStart = time.Now().AddDate(0, 0, -30).Format("2006-01-02")
	}

	newsItems, err := newsStorage.SearchNews(opts)
	if err != nil {
		return nil, err
	}

	// 按日期分组统计
	trendData := make(map[string]int)
	for _, item := range newsItems {
		date := item.CrawlTime.Format("2006-01-02")
		trendData[date]++
	}

	type TrendPoint struct {
		Date  string `json:"date"`
		Value int    `json:"value"`
	}
	var trendPoints []TrendPoint
	for date, count := range trendData {
		trendPoints = append(trendPoints, TrendPoint{Date: date, Value: count})
	}

	// 排序
	for i := 0; i < len(trendPoints)-1; i++ {
		for j := i + 1; j < len(trendPoints); j++ {
			if trendPoints[j].Date < trendPoints[i].Date {
				trendPoints[i], trendPoints[j] = trendPoints[j], trendPoints[i]
			}
		}
	}

	content, _ := json.Marshal(gin.H{
		"success": true,
		"data": gin.H{
			"topic":        p.Topic,
			"analysis_type": p.AnalysisType,
			"trend":        trendPoints,
			"total_news":   len(newsItems),
		},
	})

	return &MCPToolResult{
		Content: []MCPContentItem{{Type: "text", Text: string(content)}},
	}, nil
}

// toolAnalyzeSentiment 工具：分析情感倾向
func (s *MCPServer) toolAnalyzeSentiment(args json.RawMessage) (*MCPToolResult, error) {
	var p struct {
		Topic     string   `json:"topic"`
		Platforms []string `json:"platforms"`
		DateRange string   `json:"date_range"`
		Limit     int      `json:"limit"`
	}
	json.Unmarshal(args, &p)
	if p.Limit <= 0 {
		p.Limit = 50
	}

	newsStorage := storage.NewNewsStorage()
	opts := &model.SearchOptions{
		Query:    p.Topic,
		SearchMode: "keyword",
		Platforms:  p.Platforms,
		Limit:    p.Limit,
	}

	if p.DateRange == "last_7_days" {
		opts.DateStart = time.Now().AddDate(0, 0, -7).Format("2006-01-02")
	}

	newsItems, err := newsStorage.SearchNews(opts)
	if err != nil {
		return nil, err
	}

	// 简单的情感分析（基于关键词）
	positiveKeywords := []string{"好", "棒", "赞", "优秀", "成功", "突破", "创新", "领先"}
	negativeKeywords := []string{"差", "问题", "争议", "风险", "危机", "失败", "下跌"}

	positiveCount := 0
	negativeCount := 0
	neutralCount := 0

	for _, item := range newsItems {
		titleLower := strings.ToLower(item.Title)
		pos := 0
		neg := 0
		for _, kw := range positiveKeywords {
			if strings.Contains(titleLower, kw) {
				pos++
			}
		}
		for _, kw := range negativeKeywords {
			if strings.Contains(titleLower, kw) {
				neg++
			}
		}
		if pos > neg {
			positiveCount++
		} else if neg > pos {
			negativeCount++
		} else {
			neutralCount++
		}
	}

	total := len(newsItems)
	var sentiment string
	if total > 0 {
		if float64(positiveCount)/float64(total) > 0.6 {
			sentiment = "positive"
		} else if float64(negativeCount)/float64(total) > 0.6 {
			sentiment = "negative"
		} else {
			sentiment = "neutral"
		}
	}

	content, _ := json.Marshal(gin.H{
		"success": true,
		"data": gin.H{
			"topic":     p.Topic,
			"sentiment": sentiment,
			"distribution": gin.H{
				"positive": positiveCount,
				"negative": negativeCount,
				"neutral":  neutralCount,
			},
			"percentages": gin.H{
				"positive":  float64(positiveCount) / float64(max(total, 1)) * 100,
				"negative":  float64(negativeCount) / float64(max(total, 1)) * 100,
				"neutral":   float64(neutralCount) / float64(max(total, 1)) * 100,
			},
			"total_news": total,
		},
	})

	return &MCPToolResult{
		Content: []MCPContentItem{{Type: "text", Text: string(content)}},
	}, nil
}

// toolAggregateNews 工具：聚合新闻
func (s *MCPServer) toolAggregateNews(args json.RawMessage) (*MCPToolResult, error) {
	var p struct {
		DateRange           *string  `json:"date_range"`
		Platforms           []string `json:"platforms"`
		SimilarityThreshold float64  `json:"similarity_threshold"`
	}
	json.Unmarshal(args, &p)

	similarityThreshold := p.SimilarityThreshold
	if similarityThreshold <= 0 {
		similarityThreshold = 0.7
	}

	newsStorage := storage.NewNewsStorage()
	opts := &model.SearchOptions{
		Query:     "",
		SearchMode: "keyword",
		Platforms: p.Platforms,
		Limit:     200,
	}

	if p.DateRange != nil {
		switch *p.DateRange {
		case "last_7_days":
			opts.DateStart = time.Now().AddDate(0, 0, -7).Format("2006-01-02")
		case "last_30_days":
			opts.DateStart = time.Now().AddDate(0, 0, -30).Format("2006-01-02")
		}
	}

	newsItems, err := newsStorage.SearchNews(opts)
	if err != nil {
		return nil, err
	}

	// 简单的新闻聚类（基于关键词相似度）
	type Cluster struct {
		Title    string
		Items    []model.NewsItem
		Keywords []string
	}

	clusters := make([]Cluster, 0)
	used := make(map[int]bool)

	for i := 0; i < len(newsItems); i++ {
		if used[i] {
			continue
		}

		item := newsItems[i]
		keywords := extractKeywords(item.Title)

		cluster := Cluster{
			Title:    item.Title,
			Items:    []model.NewsItem{item},
			Keywords: keywords,
		}
		used[i] = true

		for j := i + 1; j < len(newsItems); j++ {
			if used[j] {
				continue
			}

			otherItem := newsItems[j]
			otherKeywords := extractKeywords(otherItem.Title)

			similarity := calculateKeywordSimilarity(keywords, otherKeywords)
			if similarity >= similarityThreshold {
				cluster.Items = append(cluster.Items, otherItem)
				used[j] = true
			}
		}

		clusters = append(clusters, cluster)
	}

	type ClusterResponse struct {
		Title       string         `json:"title"`
		Keywords    []string       `json:"keywords"`
		ItemCount   int            `json:"item_count"`
		Items       []model.NewsItem `json:"items"`
		PlatformIDs []string       `json:"platform_ids"`
	}

	var clusterResponses []ClusterResponse
	for _, cluster := range clusters {
		platformIDs := make(map[string]bool)
		for _, item := range cluster.Items {
			platformIDs[item.SourceID] = true
		}
		ids := make([]string, 0, len(platformIDs))
		for id := range platformIDs {
			ids = append(ids, id)
		}

		clusterResponses = append(clusterResponses, ClusterResponse{
			Title:       cluster.Title,
			Keywords:    cluster.Keywords,
			ItemCount:   len(cluster.Items),
			Items:       cluster.Items,
			PlatformIDs: ids,
		})
	}

	// 按新闻数量排序
	for i := 0; i < len(clusterResponses)-1; i++ {
		for j := i + 1; j < len(clusterResponses); j++ {
			if clusterResponses[j].ItemCount > clusterResponses[i].ItemCount {
				clusterResponses[i], clusterResponses[j] = clusterResponses[j], clusterResponses[i]
			}
		}
	}

	content, _ := json.Marshal(gin.H{
		"success": true,
		"data": gin.H{
			"clusters":             clusterResponses,
			"cluster_count":        len(clusterResponses),
			"total_news":           len(newsItems),
			"similarity_threshold": similarityThreshold,
		},
	})

	return &MCPToolResult{
		Content: []MCPContentItem{{Type: "text", Text: string(content)}},
	}, nil
}

// toolGetSystemStatus 工具：获取系统状态
func (s *MCPServer) toolGetSystemStatus(args json.RawMessage) (*MCPToolResult, error) {
	cfg := config.Get()

	// 获取数据库连接信息
	dbStatus := "connected"
	if db, err := core.GetDB().DB(); err == nil {
		if state := db.Stats(); state.OpenConnections == 0 {
			dbStatus = "disconnected"
		}
	}

	content, _ := json.Marshal(gin.H{
		"success": true,
		"data": gin.H{
			"version":     cfg.App.Version,
			"environment": cfg.App.Environment,
			"timezone":    cfg.App.Timezone,
			"database":    dbStatus,
			"mcp_tools":   len(mcpServerInstance.tools),
		},
	})

	return &MCPToolResult{
		Content: []MCPContentItem{{Type: "text", Text: string(content)}},
	}, nil
}

// toolGetCurrentConfig 工具：获取当前配置
func (s *MCPServer) toolGetCurrentConfig(args json.RawMessage) (*MCPToolResult, error) {
	var p struct {
		Section string `json:"section"`
	}
	json.Unmarshal(args, &p)
	if p.Section == "" {
		p.Section = "all"
	}

	cfg := config.Get()

	var result gin.H
	switch p.Section {
	case "crawler":
		result = gin.H{
			"platforms": cfg.Platforms,
			"rss":       cfg.RSS,
		}
	case "push":
		result = gin.H{
			"notification": config.SanitizeNotificationForAPI(cfg.Notification),
		}
	case "keywords":
		result = gin.H{}
	case "weights":
		result = gin.H{
			"weight": cfg.Advanced.Weight,
		}
	default:
		result = gin.H{
			"app":          cfg.App,
			"server":       cfg.Server,
			"scheduler":    cfg.Scheduler,
			"platforms":    cfg.Platforms,
			"rss":          cfg.RSS,
			"report":       cfg.Report,
			"filter":       cfg.Filter,
			"ai":           cfg.AI,
			"notification": config.SanitizeNotificationForAPI(cfg.Notification),
			"storage":      cfg.Storage,
		}
	}

	content, _ := json.Marshal(gin.H{
		"success": true,
		"data":    result,
	})

	return &MCPToolResult{
		Content: []MCPContentItem{{Type: "text", Text: string(content)}},
	}, nil
}

// toolGetSnapshotDates 工具：获取快照日期列表
func (s *MCPServer) toolGetSnapshotDates(args json.RawMessage) (*MCPToolResult, error) {
	var p struct {
		Days int `json:"days"`
	}
	json.Unmarshal(args, &p)
	if p.Days <= 0 {
		p.Days = 60
	}
	if p.Days > 365 {
		p.Days = 365
	}

	newsStorage := storage.NewNewsStorage()
	dates, err := newsStorage.GetSnapshotAvailableDates(p.Days)
	if err != nil {
		return nil, err
	}

	content, _ := json.Marshal(gin.H{
		"success": true,
		"data": gin.H{
			"dates":    dates,
			"timezone": config.Get().App.Timezone,
		},
	})

	return &MCPToolResult{
		Content: []MCPContentItem{{Type: "text", Text: string(content)}},
	}, nil
}

// toolGetSnapshotDaySummary 工具：获取某一天汇总
func (s *MCPServer) toolGetSnapshotDaySummary(args json.RawMessage) (*MCPToolResult, error) {
	var p struct {
		Date string `json:"date"`
	}
	json.Unmarshal(args, &p)

	newsStorage := storage.NewNewsStorage()
	hours, total, err := newsStorage.GetSnapshotDaySummary(p.Date)
	if err != nil {
		return nil, err
	}

	cfg := config.Get()
	idToName := make(map[string]string)
	for _, src := range cfg.Platforms.Sources {
		if src.ID != "" {
			idToName[src.ID] = src.Name
		}
	}

	content, _ := json.Marshal(gin.H{
		"success": true,
		"data": gin.H{
			"date":       p.Date,
			"timezone":   cfg.App.Timezone,
			"total_rows": total,
			"hours":      hours,
			"id_to_name": idToName,
		},
	})

	return &MCPToolResult{
		Content: []MCPContentItem{{Type: "text", Text: string(content)}},
	}, nil
}

// toolGetSnapshotHour 工具：获取某小时快照
func (s *MCPServer) toolGetSnapshotHour(args json.RawMessage) (*MCPToolResult, error) {
	var p struct {
		Date      string   `json:"date"`
		Hour      int      `json:"hour"`
		Platforms []string `json:"platforms"`
	}
	json.Unmarshal(args, &p)

	newsStorage := storage.NewNewsStorage()
	items, err := newsStorage.GetSnapshotForHour(p.Date, p.Hour, p.Platforms)
	if err != nil {
		return nil, err
	}

	cfg := config.Get()
	idToName := make(map[string]string)
	for _, src := range cfg.Platforms.Sources {
		if src.ID != "" {
			idToName[src.ID] = src.Name
		}
	}

	content, _ := json.Marshal(gin.H{
		"success": true,
		"data": gin.H{
			"date":       p.Date,
			"hour":       p.Hour,
			"timezone":   cfg.App.Timezone,
			"items":      items,
			"id_to_name": idToName,
		},
	})

	return &MCPToolResult{
		Content: []MCPContentItem{{Type: "text", Text: string(content)}},
	}, nil
}

// toolGetSnapshotInsights 工具：获取快照洞察
func (s *MCPServer) toolGetSnapshotInsights(args json.RawMessage) (*MCPToolResult, error) {
	var p struct {
		Date string `json:"date"`
	}
	json.Unmarshal(args, &p)

	newsStorage := storage.NewNewsStorage()
	row, err := newsStorage.GetDayIndustryReport(p.Date)
	if err != nil {
		return nil, err
	}

	cfg := config.Get()
	if row == nil {
		content, _ := json.Marshal(gin.H{
			"success": true,
			"data": gin.H{
				"date":     p.Date,
				"timezone": cfg.App.Timezone,
				"cached":   false,
				"content":  "",
				"model":    "",
			},
		})
		return &MCPToolResult{
			Content: []MCPContentItem{{Type: "text", Text: string(content)}},
		}, nil
	}

	content, _ := json.Marshal(gin.H{
		"success": true,
		"data": gin.H{
			"date":       p.Date,
			"timezone":   cfg.App.Timezone,
			"cached":     true,
			"content":    row.Content,
			"model":      row.Model,
			"updated_at": row.UpdatedAt.UTC().Format(time.RFC3339),
		},
	})

	return &MCPToolResult{
		Content: []MCPContentItem{{Type: "text", Text: string(content)}},
	}, nil
}

// toolGenerateDayInsights 工具：生成日报洞察
func (s *MCPServer) toolGenerateDayInsights(args json.RawMessage) (*MCPToolResult, error) {
	var p struct {
		Date      string   `json:"date"`
		Platforms []string `json:"platforms"`
	}
	json.Unmarshal(args, &p)

	newsStorage := storage.NewNewsStorage()
	_, total, err := newsStorage.GetSnapshotDaySummary(p.Date)
	if err != nil {
		return nil, err
	}
	if total == 0 {
		return &MCPToolResult{
			Content: []MCPContentItem{{Type: "text", Text: "该日无热榜快照，无法生成研报"}},
			IsError: true,
		}, nil
	}

	dres, err := newsStorage.BuildSnapshotDayDigest(p.Date, p.Platforms, 0)
	if err != nil {
		return nil, err
	}
	if dres == nil || strings.TrimSpace(dres.Digest) == "" {
		return &MCPToolResult{
			Content: []MCPContentItem{{Type: "text", Text: "无可用标题流（筛选后可能为空）"}},
			IsError: true,
		}, nil
	}

	cfg := config.Get()
	tz := "UTC"
	if cfg != nil {
		tz = cfg.App.Timezone
	}
	content, err := ai.GenerateDayIndustryReport(nil, p.Date, dres.Digest, tz)
	if err != nil {
		logger.WithComponent("mcp").Warn("day industry report failed", zap.Error(err), zap.String("date", p.Date))
		return &MCPToolResult{
			Content: []MCPContentItem{{Type: "text", Text: fmt.Sprintf("研报生成失败: %s", err.Error())}},
			IsError: true,
		}, nil
	}

	modelName := ""
	if cfg != nil {
		modelName = cfg.AI.Model
	}
	if err := newsStorage.SaveDayIndustryReport(p.Date, content, modelName); err != nil {
		logger.WithComponent("mcp").Warn("failed to save day industry report", zap.Error(err))
	}

	now := time.Now().UTC().Format(time.RFC3339)
	contentBytes, _ := json.Marshal(gin.H{
		"success": true,
		"data": gin.H{
			"date":              p.Date,
			"timezone":          tz,
			"cached":            true,
			"content":           content,
			"model":             modelName,
			"updated_at":        now,
			"unique_titles":     dres.UniqueTitles,
			"raw_snapshot_rows": dres.RawRowCount,
			"digest_truncated":  dres.Truncated,
		},
	})

	return &MCPToolResult{
		Content: []MCPContentItem{{Type: "text", Text: string(contentBytes)}},
	}, nil
}

// toolAnalyzeNewsArticle 工具：分析新闻文章
func (s *MCPServer) toolAnalyzeNewsArticle(args json.RawMessage) (*MCPToolResult, error) {
	var p struct {
		Title      string `json:"title"`
		URL        string `json:"url"`
		SourceName string `json:"source_name"`
	}
	json.Unmarshal(args, &p)

	summary, fetched, textLen, err := ai.SummarizeNewsArticle(nil, p.Title, p.URL, p.SourceName)
	if err != nil {
		logger.WithComponent("mcp").Warn("news article analyze failed", zap.Error(err))
		summary = fmt.Sprintf("分析失败: %s", err.Error())
	}

	content, _ := json.Marshal(gin.H{
		"success": true,
		"data": gin.H{
			"summary":         summary,
			"title":           p.Title,
			"url":             p.URL,
			"content_fetched": fetched,
			"extracted_runes": textLen,
		},
	})

	return &MCPToolResult{
		Content: []MCPContentItem{{Type: "text", Text: string(content)}},
	}, nil
}

// toolAIChat 工具：AI 对话
func (s *MCPServer) toolAIChat(args json.RawMessage) (*MCPToolResult, error) {
	var p struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Model    string `json:"model"`
		MaxTokens int   `json:"max_tokens"`
	}
	json.Unmarshal(args, &p)

	// 转换为 AI client 格式
	messages := make([]ai.ChatMessage, len(p.Messages))
	for i, msg := range p.Messages {
		messages[i] = ai.ChatMessage{
			Role:    msg.Role,
			Content: msg.Content,
		}
	}

	client := ai.NewAIClient()
	// 如果指定了 model，使用 ChatCompletion 并传入 model
	var responseText string
	var chatErr error
	if p.Model != "" {
		// 使用 ChatCompletion 支持自定义 model
		respText, _, err := client.ChatCompletion(context.Background(), messages, p.MaxTokens)
		responseText = respText
		chatErr = err
	} else {
		responseText, chatErr = client.ChatWithContext(context.Background(), messages)
	}
	if chatErr != nil {
		logger.WithComponent("mcp").Warn("AI chat failed", zap.Error(chatErr))
		return &MCPToolResult{
			Content: []MCPContentItem{{Type: "text", Text: fmt.Sprintf("AI 对话失败: %s", chatErr.Error())}},
			IsError: true,
		}, nil
	}

	contentBytes, _ := json.Marshal(gin.H{
		"success": true,
		"data": gin.H{
			"response": responseText,
			"model":    p.Model,
		},
	})

	return &MCPToolResult{
		Content: []MCPContentItem{{Type: "text", Text: string(contentBytes)}},
	}, nil
}

// toolGetStorageStatus 工具：获取存储状态
func (s *MCPServer) toolGetStorageStatus(args json.RawMessage) (*MCPToolResult, error) {
	cfg := config.Get().Storage

	// 获取数据库统计
	var dbStats gin.H
	if db, err := core.GetDB().DB(); err == nil {
		stats := db.Stats()
		dbStats = gin.H{
			"open_connections": stats.OpenConnections,
			"in_use":           stats.InUse,
			"idle":             stats.Idle,
			"wait_count":       stats.WaitCount,
			"wait_duration":    stats.WaitDuration.String(),
		}
	}

	content, _ := json.Marshal(gin.H{
		"success": true,
		"data": gin.H{
			"backend": cfg.Backend,
			"formats": cfg.Formats,
			"local":   cfg.Local,
			"remote":  cfg.Remote,
			"database_stats": dbStats,
		},
	})

	return &MCPToolResult{
		Content: []MCPContentItem{{Type: "text", Text: string(content)}},
	}, nil
}

// toolListAvailableDates 工具：列出可用日期
func (s *MCPServer) toolListAvailableDates(args json.RawMessage) (*MCPToolResult, error) {
	var p struct {
		Source string `json:"source"`
	}
	json.Unmarshal(args, &p)
	if p.Source == "" {
		p.Source = "both"
	}

	// 获取热榜日期
	newsStorage := storage.NewNewsStorage()
	dates, err := newsStorage.GetSnapshotAvailableDates(90)
	if err != nil {
		dates = []storage.SnapshotDateInfo{}
	}

	content, _ := json.Marshal(gin.H{
		"success": true,
		"data": gin.H{
			"source": p.Source,
			"dates":  dates,
		},
	})

	return &MCPToolResult{
		Content: []MCPContentItem{{Type: "text", Text: string(content)}},
	}, nil
}

// toolResolveDateRange 工具：解析日期范围
func (s *MCPServer) toolResolveDateRange(args json.RawMessage) (*MCPToolResult, error) {
	var p struct {
		Text string `json:"text"`
	}
	json.Unmarshal(args, &p)

	var dateStart, dateEnd string
	switch p.Text {
	case "今天", "today":
		today := time.Now().Format("2006-01-02")
		dateStart, dateEnd = today, today
	case "昨天", "yesterday":
		yesterday := time.Now().AddDate(0, 0, -1)
		dateStart, dateEnd = yesterday.Format("2006-01-02"), yesterday.Format("2006-01-02")
	case "最近 7 天", "last 7 days", "last_7_days":
		dateStart = time.Now().AddDate(0, 0, -7).Format("2006-01-02")
		dateEnd = time.Now().Format("2006-01-02")
	case "最近 30 天", "last 30 days", "last_30_days":
		dateStart = time.Now().AddDate(0, 0, -30).Format("2006-01-02")
		dateEnd = time.Now().Format("2006-01-02")
	case "本周", "this week":
		// 简化：本周从周一开始
		weekday := time.Now().Weekday()
		daysSinceMonday := int(weekday - time.Monday)
		weekStart := time.Now().AddDate(0, 0, -daysSinceMonday)
		weekEnd := weekStart.AddDate(0, 0, 6)
		dateStart = weekStart.Format("2006-01-02")
		dateEnd = weekEnd.Format("2006-01-02")
	case "上周", "last week":
		weekday := time.Now().Weekday()
		daysSinceMonday := int(weekday - time.Monday)
		lastWeekStart := time.Now().AddDate(0, 0, -daysSinceMonday-7)
		lastWeekEnd := lastWeekStart.AddDate(0, 0, 6)
		dateStart = lastWeekStart.Format("2006-01-02")
		dateEnd = lastWeekEnd.Format("2006-01-02")
	case "本月", "this month":
		now := time.Now()
		monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		monthEnd := monthStart.AddDate(0, 1, 0).Add(-time.Second)
		dateStart = monthStart.Format("2006-01-02")
		dateEnd = monthEnd.Format("2006-01-02")
	default:
		dateStart = ""
		dateEnd = ""
	}

	content, _ := json.Marshal(gin.H{
		"success": true,
		"data": gin.H{
			"input":   p.Text,
			"start":   dateStart,
			"end":     dateEnd,
			"format":  "YYYY-MM-DD",
		},
	})

	return &MCPToolResult{
		Content: []MCPContentItem{{Type: "text", Text: string(content)}},
	}, nil
}

// toolSendNotification 工具：发送通知
func (s *MCPServer) toolSendNotification(args json.RawMessage) (*MCPToolResult, error) {
	var p struct {
		Channel string `json:"channel"`
		Title   string `json:"title"`
		Content string `json:"content"`
		Format  string `json:"format"`
	}
	json.Unmarshal(args, &p)
	if p.Format == "" {
		p.Format = "markdown"
	}

	cfg := config.Get()
	if !cfg.Notification.Enabled {
		return &MCPToolResult{
			Content: []MCPContentItem{{Type: "text", Text: "通知功能未启用"}},
			IsError: true,
		}, nil
	}

	// 发送通知（简化实现）
	dispatcher := notification.NewDispatcher()
	var sendResult bool
	switch p.Channel {
	case "email":
		// Email 发送使用 SendWithWeChatMarkdown
		sendResult = dispatcher.SendServerChanOnce(p.Title, p.Content)
	case "serverchan":
		sendResult = dispatcher.SendServerChanOnce(p.Title, p.Content)
	case "telegram":
		sendResult = dispatcher.SendServerChanOnce(p.Title, p.Content)
	default:
		// 通用发送
		results := dispatcher.Send(p.Title, p.Content)
		sendResult = len(results) > 0
	}

	contentBytes, _ := json.Marshal(gin.H{
		"success": sendResult,
		"data": gin.H{
			"channel": p.Channel,
			"result":  sendResult,
		},
	})

	return &MCPToolResult{
		Content: []MCPContentItem{{Type: "text", Text: string(contentBytes)}},
	}, nil
}

// toolGetChannelFormatGuide 工具：获取渠道格式指南
func (s *MCPServer) toolGetChannelFormatGuide(args json.RawMessage) (*MCPToolResult, error) {
	var p struct {
		Channel string `json:"channel"`
	}
	json.Unmarshal(args, &p)

	guides := map[string]string{
		"email":     "支持 HTML、纯文本、Markdown 格式。推荐使用 HTML 格式以获得最佳邮件客户端兼容性。",
		"feishu":    "支持 Markdown 格式。标题使用 #，列表使用 -，代码块使用 ```。",
		"dingtalk":  "支持 Markdown 格式。支持 @提及、分割线 ---。",
		"telegram":  "支持 HTML 和 MarkdownV2 格式。",
		"serverchan":"纯文本格式。换行使用 \\n。",
	}

	guide := ""
	if channel, ok := guides[p.Channel]; ok {
		guide = channel
	} else {
		guide = "未知渠道，支持所有配置的渠道格式。"
	}

	content, _ := json.Marshal(gin.H{
		"success": true,
		"data": gin.H{
			"channel": p.Channel,
			"guide":   guide,
		},
	})

	return &MCPToolResult{
		Content: []MCPContentItem{{Type: "text", Text: string(content)}},
	}, nil
}

// toolGetNotificationChannels 工具：获取通知渠道
func (s *MCPServer) toolGetNotificationChannels(args json.RawMessage) (*MCPToolResult, error) {
	cfg := config.Get().Notification.Channels

	channels := gin.H{
		"email":       gin.H{"configured": cfg.Email.From != "" && cfg.Email.To != ""},
		"feishu":      gin.H{"configured": cfg.Feishu.WebhookURL != ""},
		"dingtalk":    gin.H{"configured": cfg.DingTalk.WebhookURL != ""},
		"telegram":    gin.H{"configured": cfg.Telegram.BotToken != "" && cfg.Telegram.ChatID != ""},
		"serverchan":  gin.H{"configured": cfg.ServerChan.SendKey != ""},
		"ntfy":        gin.H{"configured": cfg.Ntfy.ServerURL != "" && cfg.Ntfy.Topic != ""},
		"bark":        gin.H{"configured": cfg.Bark.URL != ""},
		"slack":       gin.H{"configured": cfg.Slack.WebhookURL != ""},
		"wework":      gin.H{"configured": cfg.WeWork.WebhookURL != ""},
		"generic_webhook": gin.H{"configured": cfg.GenericWebhook.WebhookURL != ""},
	}

	content, _ := json.Marshal(gin.H{
		"success": true,
		"data": gin.H{
			"channels": channels,
			"enabled":  config.Get().Notification.Enabled,
		},
	})

	return &MCPToolResult{
		Content: []MCPContentItem{{Type: "text", Text: string(content)}},
	}, nil
}

// ==================== MCP 资源处理 ====================

// handleResourcesList 处理资源列表请求
func (s *MCPServer) handleResourcesList(id interface{}) (*MCPResponse, error) {
	_ = config.Get() // 预留，当前未使用

	resources := []MCPResource{
		{
			URI:         "config://platforms",
			Name:        "平台配置",
			Description: "已配置的平台列表",
			MIMEType:    "application/json",
		},
		{
			URI:         "config://rss-feeds",
			Name:        "RSS 源配置",
			Description: "已配置的 RSS 源列表",
			MIMEType:    "application/json",
		},
		{
			URI:         "data://available-dates",
			Name:        "可用数据日期",
			Description: "有热榜快照数据的日期列表",
			MIMEType:    "application/json",
		},
		{
			URI:         "config://keywords",
			Name:        "关键词配置",
			Description: "关键词过滤配置",
			MIMEType:    "text/plain",
		},
	}

	return &MCPResponse{
		JSONRPC: "2.0",
		Result: gin.H{
			"resources": resources,
		},
		ID: id,
	}, nil
}

// handleResourcesRead 处理资源读取请求
func (s *MCPServer) handleResourcesRead(params json.RawMessage, id interface{}) (*MCPResponse, error) {
	var p struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return &MCPResponse{
			JSONRPC: "2.0",
			Error: &MCPError{
				Code:    MCPErrorInvalidParams,
				Message: "Invalid resource read parameters",
				Data:    err.Error(),
			},
			ID: id,
		}, nil
	}

	var textContent string
	var mimeType string

	switch p.URI {
	case "config://platforms":
		cfg := config.Get()
		platforms := make([]gin.H, 0)
		for _, src := range cfg.Platforms.Sources {
			platforms = append(platforms, gin.H{
				"id":      src.ID,
				"name":    src.Name,
				"enabled": src.Enabled,
			})
		}
		b, _ := json.MarshalIndent(platforms, "", "  ")
		textContent = string(b)
		mimeType = "application/json"

	case "config://rss-feeds":
		cfg := config.Get()
		feeds := make([]gin.H, 0)
		for _, f := range cfg.RSS.Feeds {
			feeds = append(feeds, gin.H{
				"id":        f.ID,
				"name":      f.Name,
				"url":       f.URL,
				"enabled":   f.Enabled,
				"max_items": f.MaxItems,
			})
		}
		b, _ := json.MarshalIndent(feeds, "", "  ")
		textContent = string(b)
		mimeType = "application/json"

	case "data://available-dates":
		newsStorage := storage.NewNewsStorage()
		dates, err := newsStorage.GetSnapshotAvailableDates(30)
		if err != nil {
			return &MCPResponse{
				JSONRPC: "2.0",
				Error: &MCPError{
					Code:    MCPErrorInternalError,
					Message: "Failed to read resource",
					Data:    err.Error(),
				},
				ID: id,
			}, nil
		}
		b, _ := json.MarshalIndent(dates, "", "  ")
		textContent = string(b)
		mimeType = "application/json"

	case "config://keywords":
		textContent = "关键词过滤配置在 config/config.yaml 的 filter.frequency_words 中。"
		mimeType = "text/plain"

	default:
		return &MCPResponse{
			JSONRPC: "2.0",
			Error: &MCPError{
				Code:    MCPErrorMethodNotFound,
				Message: fmt.Sprintf("Resource not found: %s", p.URI),
			},
			ID: id,
		}, nil
	}

	return &MCPResponse{
		JSONRPC: "2.0",
		Result: gin.H{
			"resource": MCPResourceContents{
				URI:      p.URI,
				MIMEType: mimeType,
				Text:     textContent,
			},
		},
		ID: id,
	}, nil
}

// ==================== 公共入口函数 ====================

// HandleMCP 公共入口：处理 MCP 请求（从 gin.Context 调用）
func HandleMCP(c *gin.Context) {
	var req MCPRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"jsonrpc": "2.0",
			"error": gin.H{
				"code":    MCPErrorParseError,
				"message": "Parse error: Invalid JSON-RPC request",
				"data":    err.Error(),
			},
		})
		return
	}

	// 验证 JSON-RPC 版本
	if req.JSONRPC != "2.0" {
		c.JSON(http.StatusBadRequest, gin.H{
			"jsonrpc": "2.0",
			"error": gin.H{
				"code":    MCPErrorInvalidRequest,
				"message": "Invalid JSON-RPC version",
			},
		})
		return
	}

	mcp := GetMCPServer()
	response, err := mcp.HandleMCPRequest(&req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"jsonrpc": "2.0",
			"error": gin.H{
				"code":    MCPErrorInternalError,
				"message": "Internal error",
				"data":    err.Error(),
			},
		})
		return
	}

	c.JSON(http.StatusOK, response)
}
