# 趋势雷达 · Go 后端

本目录为 **TrendRadar** 的 Go 实现：定时抓取多平台热榜与 RSS、可选 **AI 兴趣标题过滤**、本地 SQLite 存储、**邮件/多渠道通知**，并提供 **Gin HTTP API** 供前端与运维使用。

## 技术栈

| 组件 | 说明 |
|------|------|
| Go | 需 **1.25+**（见 `go.mod`） |
| [Gin](https://github.com/gin-gonic/gin) | HTTP 路由与中间件 |
| [GORM](https://gorm.io) | ORM，默认 SQLite，可接 PostgreSQL |
| [Viper](https://github.com/spf13/viper) | 配置与 `TRENDRADAR_` 环境变量 |
| [cron v3](https://github.com/robfig/cron) | 定时拉取与推送（秒级支持） |
| [zap](https://github.com/uber-go/zap) + [lumberjack](https://github.com/natefinch/lumberjack) | 结构化日志、按大小轮转落盘 |
| [gin-contrib/zap](https://github.com/gin-contrib/zap) | HTTP 访问日志与 Recovery |

## 新增特性（v2.0.0）

### 安全特性

#### 1. CORS 跨域支持

默认启用跨域资源共享，支持自定义允许的来源、方法、头部。

**配置示例：**
```yaml
server:
  cors:
    allow_origins: ["http://localhost:3000", "https://trendradar.example.com"]
    allow_methods: ["GET", "POST", "PUT", "DELETE", "OPTIONS"]
    allow_headers: ["Origin", "Content-Type", "Accept", "Authorization"]
    allow_credentials: true
    max_age: 43200  # 12 小时
```

**环境变量：**
```bash
TRENDRADAR_SERVER_CORS_ALLOW_ORIGINS=["http://localhost:3000"]
TRENDRADAR_SERVER_CORS_ALLOW_CREDENTIALS=true
```

#### 2. API 认证中间件

支持两级权限：**普通用户 (user)** 和 **管理员 (admin)**。

**认证方式：**
- 请求头 `X-API-Key`
- 请求头 `Authorization: Bearer <token>`

**配置示例：**
```yaml
server:
  auth:
    enabled: true
    api_key: "your-api-key-here"
    admin_api_key: "your-admin-key-here"
    token_expiry: 24h
    skip_paths:
      - "/health"
      - "/mcp"
      - "/api/v1/system/status"
```

**环境变量：**
```bash
TRENDRADAR_SERVER_AUTH_ENABLED=true
TRENDRADAR_SERVER_AUTH_API_KEY=your-secret-key
TRENDRADAR_SERVER_AUTH_ADMIN_API_KEY=your-admin-key
```

#### 3. 代理支持

爬虫支持通过 HTTP/HTTPS/SOCKS5 代理请求外部 API。

**配置示例：**
```yaml
advanced:
  crawler:
    use_proxy: true
    default_proxy: "http://proxy.example.com:8080"
  rss:
    use_proxy: true
    proxy_url: "http://proxy.example.com:8080"
```

### MCP 协议完整实现

实现 **MCP (Model Context Protocol)** JSON-RPC 2.0 协议，支持 AI 工具调用。

**支持的 MCP 方法：**
- `initialize` - 初始化连接
- `tools/list` - 获取可用工具列表
- `tools/call` - 调用工具
- `resources/list` - 获取资源列表
- `resources/read` - 读取资源内容

**支持的 MCP 工具（26 个）：**

| 类别 | 工具 | 说明 |
|------|------|------|
| 数据查询 | `get_latest_news` | 获取最新新闻 |
| 数据查询 | `get_news_by_date` | 按日期获取新闻 |
| 数据查询 | `search_news` | 搜索新闻（keyword/fuzzy/entity） |
| 数据查询 | `get_trending_topics` | 获取热门话题 |
| 数据查询 | `get_latest_rss` | 获取最新 RSS |
| 数据查询 | `search_rss` | 搜索 RSS |
| 分析 | `analyze_topic_trend` | 分析话题趋势 |
| 分析 | `analyze_sentiment` | 分析情感倾向 |
| 分析 | `aggregate_news` | 新闻聚类聚合 |
| 分析 | `compare_periods` | 对比不同时期 |
| 系统 | `get_system_status` | 获取系统状态 |
| 系统 | `get_current_config` | 获取当前配置 |
| 快照 | `get_snapshot_dates` | 获取快照日期列表 |
| 快照 | `get_snapshot_day_summary` | 获取某一天汇总 |
| 快照 | `get_snapshot_hour` | 获取某小时快照 |
| 快照 | `get_snapshot_insights` | 获取洞察报告 |
| 快照 | `generate_day_insights` | 生成日报洞察 |
| AI | `analyze_news_article` | 分析新闻文章 |
| AI | `ai_chat` | AI 对话 |
| 通知 | `send_notification` | 发送通知 |
| 通知 | `get_channel_format_guide` | 获取渠道格式指南 |
| 通知 | `get_notification_channels` | 获取通知渠道 |
| 存储 | `sync_from_remote` | 远程同步 |
| 存储 | `get_storage_status` | 获取存储状态 |
| 存储 | `list_available_dates` | 列出可用日期 |
| 工具 | `resolve_date_range` | 解析日期范围 |

**MCP 资源（4 个）：**
- `config://platforms` - 平台配置
- `config://rss-feeds` - RSS 源配置
- `data://available-dates` - 可用数据日期
- `config://keywords` - 关键词配置

### URL 标准化

实现完整的 URL 去重标准化功能，确保排名历史准确。

**功能特性：**
- 协议前缀保护（`https://`、`http://`）
- 跟踪参数过滤（UTM、share、ref 等 40+ 参数）
- 路径规范化（移除重复斜杠、尾部斜杠）
- 大小写统一
- 片段标识符移除

**测试覆盖：** 15+ 测试用例，全部通过。

### 测试覆盖

| 模块 | 测试文件 | 用例数 | 状态 |
|------|----------|--------|------|
| URL 标准化 | `news_storage_test.go` | 15+ | ✅ PASS |
| CORS 中间件 | `middleware_test.go` | 4 | ✅ PASS |
| 认证中间件 | `middleware_test.go` | 9 | ✅ PASS |
| 代理功能 | - | - | ✅ 编译通过 |

**运行测试：**
```bash
go test ./internal/storage/... ./internal/api/... -v
```

## 目录结构

```
backend-go/
├── cmd/                 # 程序入口
├── config/              # 默认 config.yaml、ai_interests.txt 等
├── docs/                 # 专题文档（分析提示词、邮件去重等）
├── internal/
│   ├── api/             # HTTP 处理器、路由
│   ├── ai/              # LLM 客户端、兴趣过滤、分析
│   ├── core/            # 数据库初始化
│   ├── crawler/         # 平台与 RSS 抓取
│   ├── notification/    # 通知下发
│   ├── scheduler/       # 定时任务、汇总邮件等
│   └── storage/         # 新闻/邮件去重等持久化
├── pkg/
│   ├── config/          # 配置结构体与加载
│   ├── logger/          # zap + 文件轮转
│   └── model/           # 数据模型
├── go.mod
└── README.md
```

## 环境准备

- 安装 **Go 1.25 或以上**（与 `go.mod` 中 `go` 行一致）。
- 在本目录执行依赖下载：

```bash
go mod download
```

## 配置

- 主配置文件默认路径：`./config/config.yaml`（相对**进程工作目录**）。
- 通过环境变量覆盖路径：

```bash
# Windows (PowerShell)
$env:CONFIG_PATH = "D:\path\to\config.yaml"

# Linux / macOS
export CONFIG_PATH=/path/to/config.yaml
```

- 使用 [Viper](https://github.com/spf13/viper) 时，可配合前缀 `TRENDRADAR_` 与环境变量点号转下划线，例如 `TRENDRADAR_SERVER_PORT=8080`（具体以 Viper 绑定为准）。
- 支持项目根下 `.env`（通过 `godotenv` 加载），**请勿**将含密钥的 `.env` 提交到版本库。

### 与密钥相关的项（务必在本地或环境变量中设置）

- `ai.api_key`、各类 Webhook/邮箱密码等，**只放在私有环境**或加密存储中；示例配置里若出现占位值，请替换为真实值。

### 与路径相关的项

- `filter.interests_file` 相对 **`config.yaml` 所在目录** 解析，默认可配合 `config/ai_interests.txt` 使用；修改后需**重启进程**以重新加载（若未实现热重载）。

更多 AI 与提示词分工、分析流程见：`docs/ai-prompts-and-analysis.md`；邮件指纹去重见：`docs/email-dedup.md`。

## 运行

在 **`backend-go` 目录**下（保证能读到 `config/config.yaml` 与 `config/ai_interests.txt` 等相对路径）：

```bash
go run ./cmd
```

或先编译再运行（见下节），将工作目录设为本目录，或使用 `CONFIG_PATH` 指到绝对路径。

**安装为系统服务**（Windows 服务 + Linux systemd，开机自启、与 `sc`/systemd 管理）：见 `docs/service-windows-linux.md`，摘要：

- 可执行文件所在目录需包含 `config/` 等；程序会自动 **以 exe 所在目录为工作目录**。
- 管理员 / root 下：`<binary> install`，再 `<binary> start`；卸载用 `uninstall`。
- 服务名一般为 **`TrendRadar`**（可搜源码中 `kardianos` 的 `Name` 字段确认）。

> 后台服务**不会**出现在任务栏/托盘；若需托盘图标，需另做客户端或计划任务在登录时启动，详见上述文档说明。

### 优雅退出

前台运行时支持 **Ctrl+C**（`SIGINT`）与 `SIGTERM`：会先停止定时调度器，再对 HTTP 做 **优雅关闭**（约 20s 内结束现有请求），再退出进程。作为 **Windows 服务** 时由服务管理器停止，同样会走同一套 `Stop` 逻辑。

### 日志

- 在 `config.yaml` 的 `logging` 节配置落盘路径、级别、是否同时输出到控制台、是否将标准库 `log` 重定向到 zap 等。
- 默认会创建 `logs/` 下的日志文件；详见 `pkg/logger/logger.go` 与配置注释。
- **排查问题**：日志为 JSON 行时，每条带 `service`、`version`、`env`（来自 `app`），以及 **`component`**（如 `scheduler`、`crawler`、`aifilter`、`ai`、`notify`、`api`、`http`、`db`）。可按 `component=aifilter` 或 `request_id=...`（HTTP，响应头 `X-Request-ID` 同值）过滤。热榜/RSS 抓取细节在 `logging.level: debug` 时更全。兴趣过滤逐条明细为 **Debug** 级别，需要时把 level 调到 `debug`。

## 编译二进制

在 `backend-go` 目录执行：

```bash
go build -o trendradar ./cmd
```

Windows 可命名 `trendradar.exe`。在仓库**根目录**时可用：

```bash
go build -o bin/trendradar.exe -C backend-go ./cmd
```

可选缩小体积、去掉部分调试路径信息：

```bash
go build -trimpath -ldflags="-s -w" -o trendradar ./cmd
```

## HTTP API 一览

基址与服务端口由 `server.host` / `server.port` 决定，默认如：`http://127.0.0.1:8080`。

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/health` | 健康检查（访问日志中默认跳过部分访问日志） |
| GET | `/api/v1/news/latest` | 最新热榜数据 |
| GET | `/api/v1/news/:date` | 按日期 |
| GET | `/api/v1/news/search` | 搜索 |
| GET | `/api/v1/news/analyze` | AI 分析文章 |
| GET | `/api/v1/topics/trending` | 话题 |
| GET | `/api/v1/rss/latest` | RSS 最新 |
| GET | `/api/v1/rss/search` | RSS 搜索 |
| GET | `/api/v1/rss/feeds/status` | 订阅状态 |
| POST | `/api/v1/analytics/topic/trend` | 分析 |
| POST | `/api/v1/analytics/sentiment` | 情感等 |
| POST | `/api/v1/analytics/aggregate` | 聚合分析 |
| GET | `/api/v1/system/status` | 系统状态 |
| GET | `/api/v1/system/config` | 当前配置（注意敏感信息暴露面） |
| POST | `/api/v1/system/crawl` | 触发抓取 |
| POST/GET | `/api/v1/storage/...` | 存储相关 |
| POST/GET | `/api/v1/ai/chat` | AI 对话 |
| GET/POST | `/mcp` | **MCP HTTP 端点**（JSON-RPC 2.0） |
| GET/POST | `/static/*` | 静态文件（如配置了 web_root） |

**安全建议：**
- 生产环境建议启用 `server.auth.enabled: true`
- 在网关层做 **鉴权、限流、TLS**
- 视情况 **勿对外暴露** `/system/config` 等敏感接口
- MCP 端点建议配置在内部网络或使用 API Key 保护

**请求头：**
- `X-API-Key` - API 认证密钥
- `Authorization: Bearer <token>` - Bearer Token 认证
- `X-Request-ID` - 请求追踪 ID

## 测试

### 运行全部测试
```bash
go test ./...
```

### 运行指定模块测试
```bash
# URL 标准化测试
go test ./internal/storage/... -v

# 中间件测试（CORS、认证）
go test ./internal/api/... -v
```

### 测试覆盖率
```bash
go test ./... -cover
```

### 测试用例统计
| 模块 | 文件 | 用例数 |
|------|------|--------|
| URL 标准化 | `storage/news_storage_test.go` | 15+ |
| CORS 中间件 | `api/middleware_test.go` | 4 |
| 认证中间件 | `api/middleware_test.go` | 9 |
| **总计** | - | **28+** |

## 新增文档

| 文件 | 内容 |
|------|------|
| `README.md`（本章） | **安全特性、MCP 协议、URL 标准化说明** |
| `docs/ai-prompts-and-analysis.md` | AI 分析、提示词与流程说明 |
| `docs/ai-filter-batching.md` | 兴趣过滤：分批、`max_input_chars`、批间隔与独立 `max_output_tokens` |
| `docs/service-windows-linux.md` | **安装为系统服务**（`install` / `sc` / `systemd`、开机自启） |
| `docs/email-dedup.md` | 邮件去重与指纹策略 |

## 安全清单

| 项目 | 状态 | 说明 |
|------|------|------|
| CORS 中间件 | ✅ 已实现 | 支持自定义允许来源 |
| API 认证 | ✅ 已实现 | 支持 API Key 和 Bearer Token |
| 管理员权限 | ✅ 已实现 | 敏感操作需要 admin 角色 |
| 配置脱敏 | ✅ 已实现 | API 返回时隐藏敏感字段 |
| 日志脱敏 | ⚠️ 注意 | AI 请求日志包含完整请求体 |
| 数据库加密 | ❌ 未实现 | 敏感数据建议加密存储 |
| TLS/HTTPS | ❌ 未实现 | 生产环境建议使用反向代理 |
| 请求限流 | ❌ 未实现 | 建议网关层实现 |

## 许可与上游

以仓库根目录声明为准；本后端为项目组成部分，配合前端与根目录 `config` 中更大套配置时，注意 **路径与部署目录** 一致。
