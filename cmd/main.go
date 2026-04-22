package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"
	"github.com/trendradar/backend-go/internal/api"
	"github.com/trendradar/backend-go/internal/core"
	"github.com/trendradar/backend-go/internal/scheduler"
	"github.com/trendradar/backend-go/pkg/config"
)

func main() {
	// 加载环境变量
	if err := godotenv.Load(".env"); err != nil {
		log.Println("No .env file found, using environment variables")
	}

	// 加载配置
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "./config/config.yaml"
	}
	if err := config.Init(configPath); err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	log.Printf("Loaded config from: %s", configPath)

	cfg := config.Get()
	log.Printf("Starting %s in %s mode", cfg.App.Name, cfg.App.Environment)

	// 初始化数据库
	if err := core.InitDatabase(); err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	// 初始化 API 服务器
	server := api.NewServer()
	jobScheduler := scheduler.NewScheduler()
	if err := jobScheduler.Start(); err != nil {
		log.Printf("Scheduler start failed: %v", err)
	}
	// 服务启动后立即执行一次：抓取 + AI 过滤分析 + 本地保存 + 邮件推送
	if jobScheduler.IsEnabled() {
		go jobScheduler.RunNow()
	}

	// 优雅关闭
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-quit
		log.Println("Shutting down server...")
		jobScheduler.Stop()
		// TODO: 优雅关闭逻辑
	}()

	// 启动服务器
	port := cfg.Server.Port
	log.Printf("Server starting on port %d", port)
	if err := server.Start(); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
