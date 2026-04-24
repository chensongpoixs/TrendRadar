# 安全配置指南

本文档介绍 TrendRadar Go 后端的安全特性配置，适用于生产环境部署。

## 目录

- [API 认证](#api-认证)
- [CORS 配置](#cors-配置)
- [代理安全](#代理安全)
- [安全清单](#安全清单)
- [最佳实践](#最佳实践)

---

## API 认证

### 启用认证

在 `config/config.yaml` 中启用认证：

```yaml
server:
  auth:
    enabled: true
    api_key: "your-production-api-key-here"
    admin_api_key: "your-production-admin-key-here"
    token_expiry: 24h
```

### 认证方式

客户端需要在请求头中传递 API Key：

```bash
# 方式 1: X-API-Key 请求头
curl -H "X-API-Key: your-api-key" http://localhost:8080/api/v1/news/latest

# 方式 2: Bearer Token
curl -H "Authorization: Bearer your-api-key" http://localhost:8080/api/v1/news/latest
```

### 权限级别

| 角色 | 权限 | 用途 |
|------|------|------|
| user | 读取数据、AI 对话、通知发送 | 前端应用、监控工具 |
| admin | 所有 user 权限 + 敏感操作 | 管理后台、运维工具 |

### 跳过路径

以下路径默认不需要认证：

```yaml
server:
  auth:
    skip_paths:
      - "/health"
      - "/mcp"
      - "/api/v1/system/status"
      - "/static/"
      - "/assets/"
```

### 通过环境变量配置

```bash
# 启用认证
export TRENDRADAR_SERVER_AUTH_ENABLED=true

# 设置 API Key（建议从密钥管理服务获取）
export TRENDRADAR_SERVER_AUTH_API_KEY=$(openssl rand -hex 32)
export TRENDRADAR_SERVER_AUTH_ADMIN_API_KEY=$(openssl rand -hex 32)
```

---

## CORS 配置

### 生产环境配置

**不要使用 `*` 通配符！**

```yaml
server:
  cors:
    allow_origins:
      - "https://app.trendradar.example.com"
      - "https://admin.trendradar.example.com"
    allow_methods:
      - "GET"
      - "POST"
      - "OPTIONS"
    allow_headers:
      - "Origin"
      - "Content-Type"
      - "Authorization"
      - "X-Request-ID"
    allow_credentials: true
    max_age: 43200
```

### 开发环境配置

```yaml
server:
  cors:
    allow_origins:
      - "http://localhost:3000"
      - "http://127.0.0.1:3000"
    allow_credentials: true
```

### 通过环境变量配置

```bash
export TRENDRADAR_SERVER_CORS_ALLOW_ORIGINS='["https://app.trendradar.example.com"]'
export TRENDRADAR_SERVER_CORS_ALLOW_CREDENTIALS=true
```

---

## 代理安全

### 代理配置

```yaml
advanced:
  crawler:
    use_proxy: true
    default_proxy: "http://proxy.internal:8080"
  rss:
    use_proxy: true
    proxy_url: "http://proxy.internal:8080"
```

### 支持的代理协议

| 协议 | 格式 | 示例 |
|------|------|------|
| HTTP | `http://host:port` | `http://proxy:8080` |
| HTTPS | `https://host:port` | `https://proxy:8080` |
| SOCKS5 | `socks5://host:port` | `socks5://proxy:1080` |

### 代理安全建议

1. **使用内部代理**：不要在公网暴露代理
2. **认证代理**：如果代理需要认证，使用 `http://user:pass@proxy:8080`
3. **监控代理流量**：记录代理访问日志

---

## 安全清单

### 已实现 ✅

| 功能 | 状态 | 说明 |
|------|------|------|
| CORS 中间件 | ✅ | 支持自定义允许来源 |
| API 认证 | ✅ | 支持 API Key 和 Bearer Token |
| 管理员权限 | ✅ | 敏感操作需要 admin 角色 |
| 配置脱敏 | ✅ | API 返回时隐藏敏感字段 |
| Request ID | ✅ | 请求追踪和日志关联 |
| 优雅关闭 | ✅ | 20 秒内结束现有请求 |

### 建议实现 ⚠️

| 功能 | 建议 | 实现位置 |
|------|------|----------|
| 请求限流 | 网关层或中间件 | Nginx / API Gateway |
| TLS/HTTPS | 使用反向代理 | Nginx / Traefik |
| IP 白名单 | 限制访问来源 | 防火墙 / 网关 |
| 审计日志 | 记录敏感操作 | 自定义中间件 |
| 数据库加密 | 加密敏感字段 | GORM Hook |

### 未实现 ❌

| 功能 | 优先级 | 说明 |
|------|--------|------|
| OAuth2 集成 | P2 | 第三方登录 |
| JWT Token | P3 | 无状态认证 |
| Rate Limiting | P1 | 防止暴力破解 |
| SQL 注入防护 | P0 | GORM 已提供基本防护 |

---

## 最佳实践

### 1. 密钥管理

```bash
# 使用环境变量（推荐）
export TRENDRADAR_SERVER_AUTH_API_KEY=$(openssl rand -hex 32)

# 或使用 .env 文件（不要提交到版本库）
# .env
TRENDRADAR_SERVER_AUTH_API_KEY=your-secret-key-here
```

### 2. 使用反向代理

```nginx
# Nginx 配置示例
server {
    listen 443 ssl;
    server_name api.trendradar.example.com;

    ssl_certificate /etc/ssl/certs/trendradar.crt;
    ssl_certificate_key /etc/ssl/private/trendradar.key;

    # 限流
    limit_req_zone $binary_remote_addr zone=api:10m rate=10r/s;
    limit_req zone=api burst=20 nodelay;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Request-ID $request_id;
    }

    # 保护敏感端点
    location /api/v1/system/config {
        allow 10.0.0.0/8;
        allow 172.16.0.0/12;
        deny all;
        proxy_pass http://127.0.0.1:8080;
    }
}
```

### 3. 安全头部

```nginx
add_header X-Content-Type-Options "nosniff" always;
add_header X-Frame-Options "DENY" always;
add_header X-XSS-Protection "1; mode=block" always;
add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;
```

### 4. 监控和告警

```bash
# 监控认证失败
grep "unauthorized" logs/trendradar.log | wc -l

# 监控异常请求
grep "forbidden" logs/trendradar.log
```

### 5. 定期轮换密钥

```bash
# 生成新的 API Key
openssl rand -hex 32

# 更新环境变量
export TRENDRADAR_SERVER_AUTH_API_KEY=new-key-here
export TRENDRADAR_SERVER_AUTH_ADMIN_API_KEY=new-admin-key-here

# 重启服务
systemctl restart trendradar
```

---

## 快速检查脚本

```bash
#!/bin/bash
# security-check.sh - 安全检查脚本

echo "=== TrendRadar 安全检查 ==="
echo ""

# 检查认证是否启用
if grep -q "enabled: true" config/config.yaml | grep -A1 "auth:"; then
    echo "✅ 认证已启用"
else
    echo "❌ 认证未启用 - 建议在生产环境启用"
fi

# 检查 CORS 配置
if grep -q "allow_origins:" config/config.yaml; then
    if grep -A1 "allow_origins:" config/config.yaml | grep -q '"*"'; then
        echo "⚠️  CORS 使用通配符 - 生产环境不建议"
    else
        echo "✅ CORS 配置了具体来源"
    fi
else
    echo "⚠️  CORS 未配置 - 使用默认值"
fi

# 检查 API Key 长度
api_key=$(grep "api_key:" config/config.yaml | awk '{print $2}' | tr -d '"')
if [ ${#api_key} -ge 32 ]; then
    echo "✅ API Key 长度足够 (${#api_key} 字符)"
else
    echo "⚠️  API Key 较短 (${#api_key} 字符) - 建议至少 32 字符"
fi

# 检查日志级别
log_level=$(grep "level:" config/config.yaml | head -1 | awk '{print $2}' | tr -d '"')
if [ "$log_level" = "debug" ]; then
    echo "⚠️  日志级别为 debug - 生产环境建议使用 info"
else
    echo "✅ 日志级别为 $log_level"
fi

echo ""
echo "=== 检查完成 ==="
```

---

## 联系与支持

如有安全问题，请通过以下方式报告：
- GitHub Issues: https://github.com/trendradar/backend-go/issues
- 安全邮箱: security@trendradar.example.com
