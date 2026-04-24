package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/trendradar/backend-go/pkg/config"
	"github.com/trendradar/backend-go/pkg/logger"
	"go.uber.org/zap"
)

// registerWebUI 若配置了 server.web_root 且存在 index.html，则托管 Vue 构建产物（/assets、SPA 回退）
func (s *Server) registerWebUI() {
	root := config.ResolveServerWebRoot()
	if root == "" {
		return
	}
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		logger.WithComponent("http").Warn("server.web_root is not a readable directory, skip static UI (deep links e.g. /news/* will 404; fix path in config)",
			zap.String("resolved_path", root), zap.Error(err))
		return
	}
	indexPath := filepath.Join(root, "index.html")
	if _, err := os.Stat(indexPath); err != nil {
		logger.WithComponent("http").Warn("server.web_root has no index.html, skip static UI (run npm run build in frontend-vue first)",
			zap.String("index_path", indexPath))
		return
	}
	assetsDir := filepath.Join(root, "assets")
	if st, err := os.Stat(assetsDir); err == nil && st.IsDir() {
		s.router.Static("/assets", assetsDir)
	}
	s.router.GET("/", func(c *gin.Context) {
		c.File(indexPath)
	})
	s.router.NoRoute(spaFallback(indexPath))
	logger.WithComponent("http").Info("serving web UI from local static files", zap.String("web_root", root))
}

func spaFallback(indexPath string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		p := c.Request.URL.Path
		if prefixAPI(p) {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		if p == "/health" || strings.HasPrefix(p, "/mcp") {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		if strings.HasPrefix(p, "/assets/") {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		c.File(indexPath)
	}
}

func prefixAPI(p string) bool {
	if p == "/api" {
		return true
	}
	return strings.HasPrefix(p, "/api/")
}
