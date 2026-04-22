// Package logger 使用 uber-go/zap 与 lumberjack 文件轮转，供全应用与标准库 log 重定向。
package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/trendradar/backend-go/pkg/config"
)

var (
	global        *zap.Logger
	stdLogRestore func()
)

// L 返回全局 zap.Logger；未 Init 时为 Nop。
func L() *zap.Logger {
	if global == nil {
		return zap.NewNop()
	}
	return global
}

// Sync 进程退出前调用，刷盘并恢复标准库 log（若曾 RedirectStdLog）。
func Sync() {
	if global != nil {
		_ = global.Sync()
	}
	if stdLogRestore != nil {
		stdLogRestore()
		stdLogRestore = nil
	}
}

// LogMeta 每条日志可带的运行元数据，便于多实例/多环境按字段筛选（Grafana、Loki、grep service=）。
type LogMeta struct {
	Service     string
	Version     string
	Environment string
}

// WithComponent 返回带统一 component 字段的子 Logger，排查时 grep component=scheduler 等。
func WithComponent(name string) *zap.Logger {
	return L().With(zap.String("component", name))
}

// Init 从配置构建日志：文件（JSON 或类 console 行）+ 可选 stderr；可选重定向标准库 log。
// meta 非空时合并 service / version / environment 到根 Logger（空串字段会跳过）。
func Init(c config.LoggingConfig, meta *LogMeta) error {
	level := parseLevel(c.Level)

	encProd := zap.NewProductionEncoderConfig()
	encProd.EncodeTime = zapcore.ISO8601TimeEncoder
	encProd.EncodeLevel = zapcore.CapitalLevelEncoder
	encProd.EncodeCaller = zapcore.ShortCallerEncoder

	var cores []zapcore.Core

	if c.Enabled {
		path := strings.TrimSpace(c.File)
		if path == "" {
			path = "logs/trendradar.log"
		}
		dir := filepath.Dir(path)
		if dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return fmt.Errorf("logger: create log dir %q: %w", dir, err)
			}
		}
		maxSize := c.MaxSizeMB
		if maxSize < 1 {
			maxSize = 100
		}
		lj := &lumberjack.Logger{
			Filename:   path,
			MaxSize:    maxSize,
			MaxBackups: c.MaxBackups,
			MaxAge:     c.MaxAgeDays,
			Compress:   c.Compress,
		}
		var fileEnc zapcore.Encoder
		if c.JSONFile {
			fileEnc = zapcore.NewJSONEncoder(encProd)
		} else {
			devCfg := zap.NewDevelopmentEncoderConfig()
			devCfg.EncodeTime = zapcore.ISO8601TimeEncoder
			devCfg.EncodeCaller = zapcore.ShortCallerEncoder
			fileEnc = zapcore.NewConsoleEncoder(devCfg)
		}
		cores = append(cores, zapcore.NewCore(fileEnc, zapcore.AddSync(lj), level))
	}

	if c.Console || !c.Enabled {
		devCfg := zap.NewDevelopmentEncoderConfig()
		devCfg.EncodeTime = zapcore.ISO8601TimeEncoder
		devCfg.EncodeCaller = zapcore.ShortCallerEncoder
		enc := zapcore.NewConsoleEncoder(devCfg)
		cores = append(cores, zapcore.NewCore(enc, zapcore.AddSync(os.Stderr), level))
	}

	if len(cores) == 0 {
		devCfg := zap.NewDevelopmentEncoderConfig()
		devCfg.EncodeTime = zapcore.ISO8601TimeEncoder
		devCfg.EncodeCaller = zapcore.ShortCallerEncoder
		enc := zapcore.NewConsoleEncoder(devCfg)
		cores = append(cores, zapcore.NewCore(enc, zapcore.AddSync(os.Stderr), level))
	}

	core := zapcore.NewTee(cores...)
	global = zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))
	if meta != nil {
		f := make([]zap.Field, 0, 3)
		if strings.TrimSpace(meta.Service) != "" {
			f = append(f, zap.String("service", strings.TrimSpace(meta.Service)))
		}
		if strings.TrimSpace(meta.Version) != "" {
			f = append(f, zap.String("version", strings.TrimSpace(meta.Version)))
		}
		if strings.TrimSpace(meta.Environment) != "" {
			f = append(f, zap.String("env", strings.TrimSpace(meta.Environment)))
		}
		if len(f) > 0 {
			global = global.With(f...)
		}
	}
	zap.ReplaceGlobals(global)

	if c.RedirectStd {
		stdLogRestore = zap.RedirectStdLog(global)
	}
	return nil
}

func parseLevel(s string) zapcore.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return zapcore.DebugLevel
	case "warn", "warning":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	case "info":
		return zapcore.InfoLevel
	default:
		return zapcore.InfoLevel
	}
}
