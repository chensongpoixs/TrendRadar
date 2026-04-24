package api

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/trendradar/backend-go/pkg/config"
)

// ==================== CORS 中间件 ====================

// CORSConfig CORS 配置
type CORSConfig struct {
	AllowOrigins     []string `mapstructure:"allow_origins"`
	AllowMethods     []string `mapstructure:"allow_methods"`
	AllowHeaders     []string `mapstructure:"allow_headers"`
	ExposeHeaders    []string `mapstructure:"expose_headers"`
	AllowCredentials bool     `mapstructure:"allow_credentials"`
	MaxAge           int      `mapstructure:"max_age"` // 预检请求缓存时间（秒）
}

// DefaultCORSConfig 默认 CORS 配置
func DefaultCORSConfig() CORSConfig {
	return CORSConfig{
		AllowOrigins:     []string{"*"},
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Accept", "Authorization", "X-Requested-With", "X-Request-ID"},
		ExposeHeaders:    []string{"X-Request-ID", "X-RateLimit-Limit", "X-RateLimit-Remaining"},
		AllowCredentials: true,
		MaxAge:           12 * 3600, // 12 小时
	}
}

// CORS 中间件：处理跨域请求
func CORS(cfg CORSConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")

		// 检查 origin 是否在允许列表中
		allowOrigin := false
		for _, o := range cfg.AllowOrigins {
			if o == "*" || o == origin {
				allowOrigin = true
				break
			}
		}

		if allowOrigin {
			// 设置 CORS 响应头
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Methods", strings.Join(cfg.AllowMethods, ", "))
			c.Header("Access-Control-Allow-Headers", strings.Join(cfg.AllowHeaders, ", "))
			c.Header("Access-Control-Expose-Headers", strings.Join(cfg.ExposeHeaders, ", "))
			c.Header("Access-Control-Allow-Credentials", strconv.FormatBool(cfg.AllowCredentials))

			// 缓存预检请求
			if cfg.MaxAge > 0 {
				c.Header("Access-Control-Max-Age", strconv.Itoa(cfg.MaxAge))
			}
		}

		// 处理 OPTIONS 预检请求
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}

// ==================== 配置加载 ====================

// corsConfigFromYAML 从配置文件中加载 CORS 配置
func corsConfigFromYAML() CORSConfig {
	defaultCfg := DefaultCORSConfig()

	v := config.GetViper()
	if v == nil {
		return defaultCfg
	}

	cfg := defaultCfg

	if origins := v.GetStringSlice("server.cors.allow_origins"); len(origins) > 0 {
		cfg.AllowOrigins = origins
	}
	if methods := v.GetStringSlice("server.cors.allow_methods"); len(methods) > 0 {
		cfg.AllowMethods = methods
	}
	if headers := v.GetStringSlice("server.cors.allow_headers"); len(headers) > 0 {
		cfg.AllowHeaders = headers
	}
	if expose := v.GetStringSlice("server.cors.expose_headers"); len(expose) > 0 {
		cfg.ExposeHeaders = expose
	}
	if creds := v.GetBool("server.cors.allow_credentials"); creds {
		cfg.AllowCredentials = creds
	}
	if maxAge := v.GetInt("server.cors.max_age"); maxAge > 0 {
		cfg.MaxAge = maxAge
	}

	return cfg
}

// ==================== 便捷函数 ====================

// NewCORS 创建 CORS 中间件（自动从配置加载）
func NewCORS() gin.HandlerFunc {
	return CORS(corsConfigFromYAML())
}

// CORSWithOrigins 创建仅允许特定 origin 的 CORS 中间件（开发用）
func CORSWithOrigins(origins ...string) gin.HandlerFunc {
	cfg := DefaultCORSConfig()
	cfg.AllowOrigins = origins
	if len(origins) == 0 {
		cfg.AllowOrigins = []string{"*"}
	}
	return CORS(cfg)
}

// CORSDevMode 开发模式 CORS（允许所有来源，安全模式关闭）
func CORSDevMode() gin.HandlerFunc {
	cfg := DefaultCORSConfig()
	cfg.AllowOrigins = []string{"*"}
	cfg.AllowCredentials = false
	return CORS(cfg)
}
