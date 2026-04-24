package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/trendradar/backend-go/pkg/config"
	"github.com/trendradar/backend-go/pkg/logger"
	"go.uber.org/zap"
)

// ==================== API 认证中间件 ====================

// AuthConfig 认证配置
type AuthConfig struct {
	Enabled        bool      `mapstructure:"enabled"`
	APIKey         string    `mapstructure:"api_key"`
	TokenExpiry    time.Duration `mapstructure:"token_expiry"`
	SkipPaths      []string  `mapstructure:"skip_paths"`
	AdminAPIKey    string    `mapstructure:"admin_api_key"`
}

// DefaultAuthConfig 默认认证配置
func DefaultAuthConfig() AuthConfig {
	return AuthConfig{
		Enabled:     false, // 默认关闭，生产环境开启
		TokenExpiry: 24 * time.Hour,
		SkipPaths: []string{
			"/health",
			"/mcp",
			"/api/v1/system/status",
			"/api/v1/system/config",
			"/static/",
			"/assets/",
		},
	}
}

// AuthMiddleware 认证中间件
func AuthMiddleware(cfg AuthConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 跳过不需要认证的路径
		path := c.Request.URL.Path
		for _, skipPath := range cfg.SkipPaths {
			if strings.HasPrefix(path, skipPath) {
				c.Next()
				return
			}
		}

		// 获取 API Key
		apiKey := c.GetHeader("X-API-Key")
		if apiKey == "" {
			apiKey = c.GetHeader("Authorization")
			if strings.HasPrefix(apiKey, "Bearer ") {
				apiKey = strings.TrimPrefix(apiKey, "Bearer ")
			}
		}

		// 检查 API Key 有效性
		if apiKey == "" {
			unauthorized(c, "缺少 API Key")
			return
		}

		// 验证 API Key
		isValid := false
		isAdmin := false
		if apiKey == cfg.APIKey {
			isValid = true
		}
		if cfg.AdminAPIKey != "" && apiKey == cfg.AdminAPIKey {
			isValid = true
			isAdmin = true
		}

		if !isValid {
			unauthorized(c, "无效的 API Key")
			return
		}

		// 设置用户角色到 context
		role := "user"
		if isAdmin {
			role = "admin"
		}
		c.Set("api_key_role", role)
		c.Set("api_key_valid", true)

		logger.WithComponent("auth").Debug("API key validated",
			zap.String("path", path),
			zap.String("role", role),
		)

		c.Next()
	}
}

// RequireAdmin 要求管理员权限的中间件
func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		role, exists := c.Get("api_key_role")
		if !exists || role != "admin" {
			forbidden(c, "需要管理员权限")
			return
		}
		c.Next()
	}
}

// RequireAPIKey 要求 API Key 的中间件（不验证具体值）
func RequireAPIKey() gin.HandlerFunc {
	return func(c *gin.Context) {
		apiKey := c.GetHeader("X-API-Key")
		if apiKey == "" {
			unauthorized(c, "需要 API Key")
			return
		}
		c.Next()
	}
}

// ==================== 响应辅助函数 ====================

func unauthorized(c *gin.Context, message string) {
	c.JSON(http.StatusUnauthorized, gin.H{
		"success": false,
		"error":   "unauthorized",
		"message": message,
	})
	c.Abort()
}

func forbidden(c *gin.Context, message string) {
	c.JSON(http.StatusForbidden, gin.H{
		"success": false,
		"error":   "forbidden",
		"message": message,
	})
	c.Abort()
}

// ==================== 配置加载 ====================

// authConfigFromYAML 从配置文件中加载认证配置
func authConfigFromYAML() AuthConfig {
	defaultCfg := DefaultAuthConfig()

	v := config.GetViper()
	if v == nil {
		return defaultCfg
	}

	cfg := defaultCfg

	if enabled := v.GetBool("server.auth.enabled"); enabled {
		cfg.Enabled = enabled
	}
	if apiKey := v.GetString("server.auth.api_key"); apiKey != "" {
		cfg.APIKey = apiKey
	}
	if adminKey := v.GetString("server.auth.admin_api_key"); adminKey != "" {
		cfg.AdminAPIKey = adminKey
	}
	if expiry := v.GetDuration("server.auth.token_expiry"); expiry > 0 {
		cfg.TokenExpiry = expiry
	}
	if skipPaths := v.GetStringSlice("server.auth.skip_paths"); len(skipPaths) > 0 {
		cfg.SkipPaths = skipPaths
	}

	return cfg
}

// ==================== 便捷函数 ====================

// NewAuthMiddleware 创建认证中间件（自动从配置加载）
func NewAuthMiddleware() gin.HandlerFunc {
	cfg := authConfigFromYAML()
	if !cfg.Enabled {
		return func(c *gin.Context) {
			c.Next()
		}
	}
	return AuthMiddleware(cfg)
}

// NewAdminOnlyMiddleware 创建仅管理员可访问的中间件
func NewAdminOnlyMiddleware() gin.HandlerFunc {
	cfg := authConfigFromYAML()
	if !cfg.Enabled || cfg.AdminAPIKey == "" {
		return func(c *gin.Context) {
			c.Next()
		}
	}
	return AuthMiddleware(cfg)
}
