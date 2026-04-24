# 变更日志 (CHANGELOG)

## [v2.0.0] - 2026-04-25

### 新增特性

#### 🔴 安全增强

**1. CORS 跨域中间件**
- 新增 `internal/api/middleware_cors.go`
- 支持自定义允许来源、方法、头部
- 预检请求缓存（默认 12 小时）
- 从配置文件 `server.cors.*` 加载
- 支持 `*` 通配符允许所有来源
- 已集成到服务器启动流程

**2. API 认证中间件**
- 新增 `internal/api/middleware_auth.go`
- 支持两级权限：普通用户 (user) 和 管理员 (admin)
- 认证方式：`X-API-Key` 请求头或 `Authorization: Bearer <token>`
- 管理员专用中间件 `RequireAdmin()` 保护敏感操作
- 可配置跳过路径（健康检查、MCP 端点等）
- 默认关闭，生产环境启用

**3. 代理支持**
- 修改 `internal/crawler/platform_crawler.go`
- 支持 HTTP、HTTPS、SOCKS5 代理协议
- 自动添加协议前缀（如果缺失）
- 使用 `http.Transport.Proxy` 配置
- 配置项：`advanced.crawler.use_proxy`、`advanced.crawler.default_proxy`

#### 🟡 MCP 协议完整实现

**新增 `internal/api/mcp_protocol.go`**
- 实现 MCP (Model Context Protocol) JSON-RPC 2.0 协议
- 支持方法：
  - `initialize` - 初始化连接
  - `tools/list` - 获取工具列表
  - `tools/call` - 调用工具
  - `resources/list` - 获取资源列表
  - `resources/read` - 读取资源内容
- 实现 26 个 MCP 工具（完整迁移自 API 端点）
- 实现 4 个 MCP 资源
- 所有工具返回 `{success, data, error}` 格式

#### 🟡 URL 标准化

**修改 `internal/storage/news_storage.go`**
- 实现完整的 URL 去重标准化功能
- 功能特性：
  - 协议前缀保护（`https://`、`http://`）
  - 跟踪参数过滤（UTM、share、ref 等 40+ 参数）
  - 路径规范化（移除重复斜杠、尾部斜杠）
  - 大小写统一
  - 片段标识符移除
- 新增辅助函数：
  - `IsURLTracked()` - 检查 URL 是否包含跟踪参数
  - `ExtractBaseURL()` - 提取 URL 基础部分

#### 🟡 测试覆盖

**新增测试文件：**
- `internal/storage/news_storage_test.go` - URL 标准化测试（15+ 用例）
- `internal/api/middleware_test.go` - 中间件测试（13 用例）

**测试统计：**
| 模块 | 用例数 | 状态 |
|------|--------|------|
| URL 标准化 | 15+ | ✅ PASS |
| CORS 中间件 | 4 | ✅ PASS |
| 认证中间件 | 9 | ✅ PASS |
| **总计** | **28+** | **100%** |

### 配置变更

#### 新增配置项

```yaml
# CORS 配置
server:
  cors:
    allow_origins: ["*"]  # 默认允许所有
    allow_methods: ["GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"]
    allow_headers: ["Origin", "Content-Type", "Accept", "Authorization", "X-Requested-With", "X-Request-ID"]
    expose_headers: ["X-Request-ID", "X-RateLimit-Limit", "X-RateLimit-Remaining"]
    allow_credentials: true
    max_age: 43200  # 12 小时

# 认证配置
server:
  auth:
    enabled: false  # 默认关闭
    api_key: ""
    admin_api_key: ""
    token_expiry: 24h
    skip_paths:
      - "/health"
      - "/mcp"
      - "/api/v1/system/status"
      - "/static/"
      - "/assets/"
```

#### 环境变量支持

```bash
# CORS
TRENDRADAR_SERVER_CORS_ALLOW_ORIGINS=["http://localhost:3000"]
TRENDRADAR_SERVER_CORS_ALLOW_CREDENTIALS=true

# 认证
TRENDRADAR_SERVER_AUTH_ENABLED=true
TRENDRADAR_SERVER_AUTH_API_KEY=your-secret-key
TRENDRADAR_SERVER_AUTH_ADMIN_API_KEY=your-admin-key

# 代理
TRENDRADAR_ADVANCED_CRAWLER_USE_PROXY=true
TRENDRADAR_ADVANCED_CRAWLER_DEFAULT_PROXY=http://proxy:8080
```

### 文件变更清单

| 文件 | 操作 | 说明 |
|------|------|------|
| `internal/api/mcp_protocol.go` | 新增 | MCP 协议完整实现（~1800 行） |
| `internal/api/middleware_cors.go` | 新增 | CORS 中间件（~130 行） |
| `internal/api/middleware_auth.go` | 新增 | API 认证中间件（~170 行） |
| `internal/api/middleware_test.go` | 新增 | 中间件测试（~200 行） |
| `internal/storage/news_storage.go` | 修改 | URL 标准化功能（+80 行） |
| `internal/storage/news_storage_test.go` | 新增 | URL 测试（~160 行） |
| `internal/crawler/platform_crawler.go` | 修改 | 代理功能（+30 行） |
| `internal/api/server.go` | 修改 | 集成 CORS 和认证中间件 |
| `README.md` | 修改 | 更新文档 |
| `CHANGELOG.md` | 新增 | 变更日志 |

### 安全建议

1. **生产环境务必启用认证**：`server.auth.enabled: true`
2. **使用强 API Key**：建议 32 位以上随机字符串
3. **限制 CORS 来源**：不要在生产环境使用 `*`
4. **配置管理员密钥**：保护敏感操作（如配置修改）
5. **使用反向代理**：在网关层实现 TLS 和限流
6. **监控日志**：关注认证失败的请求

### 兼容性

- ✅ 向后兼容：默认关闭认证和 CORS 自定义
- ✅ 平滑升级：无需修改现有代码
- ⚠️ 注意：启用认证后，客户端需要传递 API Key

---

## [v1.5.0] - 之前版本

### 主要特性
- MCP 协议空壳实现
- 基础爬虫功能
- SQLite/PostgreSQL 双后端
- AI 兴趣过滤
- 多渠道通知
- 系统服务封装
