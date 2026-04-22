package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"
	"github.com/trendradar/backend-go/internal/api"
	"github.com/trendradar/backend-go/internal/core"
	"github.com/trendradar/backend-go/pkg/config"
)

func main() {
	// 加载环境变量
	if err := godotenv.Load(".env"); err != nil {
		log.Println("No .env file found, using environment variables")
	}

	// 加载配置
	configPath := os.Getenv("CONFIG_PATH")
	if err := config.Init(configPath); err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	cfg := config.Get()
	log.Printf("Starting %s in %s mode", cfg.App.Name, cfg.App.Environment)

	// 初始化数据库
	if err := core.InitDatabase(); err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	// 初始化 API 服务器
	server := api.NewServer()

	// 优雅关闭
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-quit
		log.Println("Shutting down server...")
		// TODO: 优雅关闭逻辑
	}()

	// 启动服务器
	port := cfg.Server.Port
	log.Printf("Server starting on port %d", port)
	if err := server.Start(); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
