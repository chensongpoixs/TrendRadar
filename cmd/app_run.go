package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/joho/godotenv"
	"github.com/trendradar/backend-go/internal/api"
	"github.com/trendradar/backend-go/internal/core"
	"github.com/trendradar/backend-go/internal/scheduler"
	"github.com/trendradar/backend-go/pkg/config"
	"github.com/trendradar/backend-go/pkg/logger"
	"go.uber.org/zap"
)

// chdirToExecutable 将工作目录设为可执行文件所在目录，保证相对路径 config/、.env、data 在「服务/开机」场景下仍正确。
func chdirToExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(exe)
	if err := os.Chdir(dir); err != nil {
		return "", err
	}
	return dir, nil
}

// runApp 在 ctx 取消时优雅关闭（供前台运行与 kardianos 服务共用）。
func runApp(ctx context.Context) error {
	if err := godotenv.Load(".env"); err != nil {
		// 未打日志前仍可用 stderr
		fmt.Fprintln(os.Stderr, "no .env, using process environment")
	}

	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "./config/config.yaml"
	}
	if err := config.Init(configPath); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	cfg := config.Get()
	if err := logger.Init(cfg.Logging, &logger.LogMeta{
		Service:     cfg.App.Name,
		Version:     cfg.App.Version,
		Environment: cfg.App.Environment,
	}); err != nil {
		return fmt.Errorf("logger: %w", err)
	}
	defer logger.Sync()

	logger.L().Info("config loaded", zap.String("path", configPath))
	logger.L().Info("starting", zap.String("app", cfg.App.Name), zap.String("env", cfg.App.Environment))

	if err := core.InitDatabase(); err != nil {
		return fmt.Errorf("database: %w", err)
	}

	server := api.NewServer()
	jobScheduler := scheduler.NewScheduler()
	if err := jobScheduler.Start(); err != nil {
		logger.L().Error("scheduler start failed", zap.Error(err))
	}
	if jobScheduler.IsEnabled() {
		go jobScheduler.RunNow()
	}

	errCh := make(chan error, 1)
	go func() { errCh <- server.Start() }()

	logger.L().Info("http server starting", zap.Int("port", cfg.Server.Port))

	select {
	case <-ctx.Done():
		logger.L().Info("shutdown from context", zap.Error(ctx.Err()))
		jobScheduler.Stop()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.L().Warn("graceful shutdown error", zap.Error(err))
		} else {
			logger.L().Info("http server shut down")
		}
		if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.L().Error("http server exit", zap.Error(err))
		}
		return nil
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
		jobScheduler.Stop()
		return nil
	}
}
