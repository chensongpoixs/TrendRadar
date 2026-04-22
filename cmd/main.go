package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/trendradar/backend-go/internal/api"
	"github.com/trendradar/backend-go/internal/core"
	"github.com/trendradar/backend-go/internal/scheduler"
	"github.com/trendradar/backend-go/pkg/config"
	"github.com/trendradar/backend-go/pkg/logger"
	"go.uber.org/zap"
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
	cfg := config.Get()
	if err := logger.Init(cfg.Logging, &logger.LogMeta{
		Service:     cfg.App.Name,
		Version:     cfg.App.Version,
		Environment: cfg.App.Environment,
	}); err != nil {
		log.Fatalf("Failed to init logger: %v", err)
	}
	defer logger.Sync()

	logger.L().Info("config loaded", zap.String("path", configPath))
	logger.L().Info("starting", zap.String("app", cfg.App.Name), zap.String("env", cfg.App.Environment))

	if err := core.InitDatabase(); err != nil {
		logger.L().Fatal("failed to initialize database", zap.Error(err))
	}

	// 初始化 API 服务器
	server := api.NewServer()
	jobScheduler := scheduler.NewScheduler()
	if err := jobScheduler.Start(); err != nil {
		logger.L().Error("scheduler start failed", zap.Error(err))
	}
	// 服务启动后立即执行一次：抓取 + AI 过滤分析 + 本地保存 + 邮件推送
	if jobScheduler.IsEnabled() {
		go jobScheduler.RunNow()
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Start()
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	port := cfg.Server.Port
	logger.L().Info("http server starting", zap.Int("port", port))

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.L().Fatal("http server stopped with error", zap.Error(err))
		}
		jobScheduler.Stop()
	case sig := <-sigCh:
		signal.Stop(sigCh)
		logger.L().Info("shutdown signal received", zap.String("signal", sig.String()))
		jobScheduler.Stop()

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.L().Warn("graceful shutdown finished with error", zap.Error(err))
		} else {
			logger.L().Info("http server shut down gracefully")
		}
		if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.L().Error("http server exit", zap.Error(err))
		}
	}
}
