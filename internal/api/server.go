package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	ginzap "github.com/gin-contrib/zap"
	"github.com/gin-gonic/gin"
	"github.com/trendradar/backend-go/pkg/config"
	"github.com/trendradar/backend-go/pkg/logger"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Server API 服务器
type Server struct {
	router  *gin.Engine
	httpSrv *http.Server
}

// NewServer 创建服务器实例
func NewServer() *Server {
	cfg := config.Get()

	// 设置 Gin 模式
	switch cfg.Server.Mode {
	case "release":
		gin.SetMode(gin.ReleaseMode)
	case "test":
		gin.SetMode(gin.TestMode)
	default:
		gin.SetMode(gin.DebugMode)
	}

	router := gin.New()
	zl := logger.L()
	router.Use(RequestIDMiddleware())
	router.Use(ginzap.GinzapWithConfig(zl, &ginzap.Config{
		TimeFormat:   time.RFC3339,
		UTC:          false,
		DefaultLevel: zapcore.InfoLevel,
		SkipPaths:    []string{"/health"},
		Context: func(c *gin.Context) []zapcore.Field {
			if v, ok := c.Get(RequestIDContextKey); ok {
				if s, ok2 := v.(string); ok2 && s != "" {
					return []zapcore.Field{zap.String("request_id", s)}
				}
			}
			return nil
		},
	}))
	router.Use(ginzap.RecoveryWithZap(zl, true))

	// TODO: 添加 CORS 中间件
	// TODO: 添加认证中间件

	s := &Server{router: router}

	// 注册路由（API 等固定路径须先注册）
	s.registerRoutes()
	// 托管本地前端构建目录 + SPA history 回退（未配置 web_root 则跳过）
	s.registerWebUI()

	return s
}

// registerRoutes 注册路由
func (s *Server) registerRoutes() {
	// 健康检查
	s.router.GET("/health", healthCheck)

	// API v1 路由
	v1 := s.router.Group("/api/v1")
	{
		// 新闻相关
		news := v1.Group("/news")
		{
			news.GET("/latest", GetLatestNews)
			news.GET("/search", SearchNews)
			news.POST("/analyze", PostAnalyzeNewsArticle)
			news.GET("/snapshots/dates", GetSnapshotAvailableDates)
			news.GET("/snapshots/:date/summary", GetSnapshotDaySummary)
			news.GET("/snapshots/:date/hour/:hour", GetSnapshotHour)
			news.GET("/snapshots/:date/insights", GetSnapshotDayInsights)
			news.POST("/snapshots/:date/insights", PostSnapshotDayInsights)
			news.GET("/:date", GetNewsByDate)
		}

		// 大模型对话（后端转发，key 不暴露给前端）
		v1.POST("/ai/chat", PostAIChat)

		// 话题统计
		v1.GET("/topics/trending", GetTrendingTopics)

		// RSS
		rss := v1.Group("/rss")
		{
			rss.GET("/latest", GetLatestRSS)
			rss.GET("/search", SearchRSS)
			rss.GET("/feeds/status", GetRSSFeedsStatus)
		}

		// 分析
		analytics := v1.Group("/analytics")
		{
			analytics.POST("/topic/trend", AnalyzeTopicTrend)
			analytics.POST("/sentiment", AnalyzeSentiment)
			analytics.POST("/aggregate", AggregateNews)
		}

		// 系统
		v1.GET("/system/status", GetSystemStatus)
		v1.GET("/system/config", GetCurrentConfig)
		v1.POST("/system/crawl", TriggerCrawl)

		// 存储
		storage := v1.Group("/storage")
		{
			storage.POST("/sync", SyncFromRemote)
			storage.GET("/status", GetStorageStatus)
			storage.GET("/dates", ListAvailableDates)
		}

		// 每日新闻导出（手动触发），POST body: {"date": "2026-04-28"}，留空为当天
		v1.POST("/daily-export", PostDailyExport)
	}

	// MCP HTTP 端点
	s.router.GET("/mcp", MCPHandle)
	s.router.POST("/mcp", MCPHandle)
}

// Start 在 ListenAndServe 上阻塞，直至 Shutdown 或监听失败。勿与 gin.Run 混用。
func (s *Server) Start() error {
	cfg := config.Get()
	addr := cfg.Server.Host + ":" + fmt.Sprintf("%d", cfg.Server.Port)

	s.httpSrv = &http.Server{
		Addr:    addr,
		Handler: s.router,
	}
	logger.WithComponent("http").Info("server listening", zap.String("addr", addr))
	return s.httpSrv.ListenAndServe()
}

// Shutdown 优雅停止 HTTP 服务，关闭监听器与空闲连接。ctx 可设总超时（如 20s）。
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpSrv == nil {
		return nil
	}
	return s.httpSrv.Shutdown(ctx)
}

// healthCheck 健康检查
func healthCheck(c *gin.Context) {
	c.JSON(200, gin.H{
		"status":  "ok",
		"message": "趋势雷达 API is running",
	})
}
