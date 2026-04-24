package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// ==================== CORS 中间件测试 ====================

func TestCORS_Middleware(t *testing.T) {
	cfg := DefaultCORSConfig()
	cfg.AllowOrigins = []string{"http://localhost:8080"}
	cfg.AllowCredentials = true

	handler := CORS(cfg)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("OPTIONS", "http://example.com/api", nil)
	c.Request.Header.Set("Origin", "http://localhost:8080")

	handler(c)

	if w.Code != 204 {
		t.Errorf("Expected status 204, got %d", w.Code)
	}

	// 检查 CORS 头
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:8080" {
		t.Errorf("Expected Access-Control-Allow-Origin to be 'http://localhost:8080', got '%s'", got)
	}

	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("Expected Access-Control-Allow-Credentials to be 'true', got '%s'", got)
	}
}

func TestCORS_AllowAllOrigins(t *testing.T) {
	cfg := CORSConfig{
		AllowOrigins: []string{"*"},
	}

	handler := CORS(cfg)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "http://example.com/api", nil)
	c.Request.Header.Set("Origin", "http://any-origin.com")

	handler(c)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://any-origin.com" {
		t.Errorf("Expected Access-Control-Allow-Origin to be 'http://any-origin.com', got '%s'", got)
	}
}

// ==================== 认证中间件测试 ====================

func TestAuthMiddleware_NoAPIKey(t *testing.T) {
	cfg := AuthConfig{
		Enabled: true,
		APIKey:  "test-api-key",
		SkipPaths: []string{"/health"},
	}

	handler := AuthMiddleware(cfg)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/v1/news", nil)

	handler(c)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected status 401, got %d", w.Code)
	}
}

func TestAuthMiddleware_InvalidAPIKey(t *testing.T) {
	cfg := AuthConfig{
		Enabled: true,
		APIKey:  "test-api-key",
		SkipPaths: []string{"/health"},
	}

	handler := AuthMiddleware(cfg)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/v1/news", nil)
	c.Request.Header.Set("X-API-Key", "wrong-key")

	handler(c)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected status 401, got %d", w.Code)
	}
}

func TestAuthMiddleware_ValidAPIKey(t *testing.T) {
	cfg := AuthConfig{
		Enabled: true,
		APIKey:  "test-api-key",
		SkipPaths: []string{"/health"},
	}

	handler := AuthMiddleware(cfg)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/v1/news", nil)
	c.Request.Header.Set("X-API-Key", "test-api-key")

	handler(c)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	// 检查角色是否设置
	role, exists := c.Get("api_key_role")
	if !exists || role != "user" {
		t.Errorf("Expected api_key_role to be 'user', got '%v'", role)
	}
}

func TestAuthMiddleware_AdminAPIKey(t *testing.T) {
	cfg := AuthConfig{
		Enabled: true,
		APIKey:  "user-key",
		AdminAPIKey: "admin-key",
		SkipPaths: []string{"/health"},
	}

	handler := AuthMiddleware(cfg)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/v1/news", nil)
	c.Request.Header.Set("X-API-Key", "admin-key")

	handler(c)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	role, exists := c.Get("api_key_role")
	if !exists || role != "admin" {
		t.Errorf("Expected api_key_role to be 'admin', got '%v'", role)
	}
}

func TestAuthMiddleware_SkipPath(t *testing.T) {
	cfg := AuthConfig{
		Enabled: true,
		APIKey:  "test-api-key",
		SkipPaths: []string{"/health"},
	}

	handler := AuthMiddleware(cfg)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/health", nil)

	handler(c)

	// 应该直接通过，不返回 401
	if w.Code == http.StatusUnauthorized {
		t.Error("Expected to skip auth for /health path")
	}
}

func TestAuthMiddleware_BearerToken(t *testing.T) {
	cfg := AuthConfig{
		Enabled: true,
		APIKey:  "test-api-key",
		SkipPaths: []string{"/health"},
	}

	handler := AuthMiddleware(cfg)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/v1/news", nil)
	c.Request.Header.Set("Authorization", "Bearer test-api-key")

	handler(c)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200 with Bearer token, got %d", w.Code)
	}
}

// ==================== RequireAdmin 测试 ====================

func TestRequireAdmin_NoRole(t *testing.T) {
	handler := RequireAdmin()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/v1/admin", nil)

	handler(c)

	if w.Code != http.StatusForbidden {
		t.Errorf("Expected status 403, got %d", w.Code)
	}
}

func TestRequireAdmin_UserRole(t *testing.T) {
	handler := RequireAdmin()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/v1/admin", nil)
	c.Set("api_key_role", "user")

	handler(c)

	if w.Code != http.StatusForbidden {
		t.Errorf("Expected status 403 for user role, got %d", w.Code)
	}
}

func TestRequireAdmin_AdminRole(t *testing.T) {
	handler := RequireAdmin()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/v1/admin", nil)
	c.Set("api_key_role", "admin")

	handler(c)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200 for admin role, got %d", w.Code)
	}
}
