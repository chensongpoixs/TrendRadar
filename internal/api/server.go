package api

import (
	"fmt"
	"log"

	"github.com/gin-gonic/gin"
	"github.com/trendradar/backend-go/pkg/config"
)

// Server API 服务器
type Server struct {
	router *gin.Engine
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
	router.Use(gin.Logger())
	router.Use(gin.Recovery())

	// TODO: 添加 CORS 中间件
	// TODO: 添加认证中间件

	s := &Server{router: router}

	// 注册路由
	s.registerRoutes()

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
			news.GET("/:date", GetNewsByDate)
			news.GET("/search", SearchNews)
		}

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
	}

	// MCP HTTP 端点
	s.router.GET("/mcp", MCPHandle)
	s.router.POST("/mcp", MCPHandle)
}

// Start 启动服务器
func (s *Server) Start() error {
	cfg := config.Get()
	addr := cfg.Server.Host + ":" + fmt.Sprintf("%d", cfg.Server.Port)

	log.Printf("Server listening on %s", addr)
	return s.router.Run(addr)
}

// healthCheck 健康检查
func healthCheck(c *gin.Context) {
	c.JSON(200, gin.H{
		"status":  "ok",
		"message": "TrendRadar API is running",
	})
}
