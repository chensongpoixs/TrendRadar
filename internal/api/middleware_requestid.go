package api

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/gin-gonic/gin"
)

// RequestIDContextKey gin.Context 中 request_id 的 key，供访问日志等读取。
const RequestIDContextKey = "request_id"

func randomRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "req-unknown"
	}
	return hex.EncodeToString(b[:])
}

// RequestIDMiddleware 注入 X-Request-ID（可透传客户端），并写入 gin.Context 供访问日志关联。
func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		rid := c.GetHeader("X-Request-ID")
		if rid == "" {
			rid = randomRequestID()
		}
		c.Set(RequestIDContextKey, rid)
		c.Writer.Header().Set("X-Request-ID", rid)
		c.Next()
	}
}
