package config

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
)

// Config 根配置结构
type Config struct {
	App           AppConfig
	Server        ServerConfig
	Logging       LoggingConfig `mapstructure:"logging"`
	Database      DatabaseConfig
	Scheduler     SchedulerConfig
	Platforms     PlatformConfig
	RSS           RSSConfig
	Report        ReportConfig
	Filter        FilterConfig
	AI            AIConfig
	AIAnalysis    AIAnalysisConfig
	AIFilter      AIFilterConfig `mapstructure:"ai_filter"`
	AITranslation AITranslationConfig
	Notification  NotificationConfig
	Storage       StorageConfig
	Advanced      AdvancedConfig
	DailyExport   DailyExportConfig `mapstructure:"daily_export"`
}

// AppConfig 应用配置
type AppConfig struct {
	Name              string `mapstructure:"name"`
	Environment       string `mapstructure:"environment"`
	Timezone          string `mapstructure:"timezone"`
	Version           string `mapstructure:"version"`
	ShowVersionUpdate bool   `mapstructure:"show_version_update"`
}

// ServerConfig 服务器配置
type ServerConfig struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`
	Mode string `mapstructure:"mode"` // debug/release/test
	// WebRoot 非空时从该目录托管已构建的前端静态资源（如 frontend-vue 的 dist），与 API 同端口；空则仅提供 API。
	// 相对路径相对「config.yaml 所在目录」解析（与 filter.interests_file 一致）。
	WebRoot string `mapstructure:"web_root"`
}

// LoggingConfig 日志（zap + lumberjack 轮转文件，见 pkg/logger）
type LoggingConfig struct {
	Enabled     bool   `mapstructure:"enabled"`
	Level       string `mapstructure:"level"` // debug, info, warn, error
	File        string `mapstructure:"file"`  // 主日志文件路径；空则用默认 ./logs/trendradar.log
	MaxSizeMB   int    `mapstructure:"max_size_mb"`
	MaxBackups  int    `mapstructure:"max_backups"`
	MaxAgeDays  int    `mapstructure:"max_age_days"`
	Compress    bool   `mapstructure:"compress"`
	Console     bool   `mapstructure:"console"`      // 是否同时输出到 stderr
	JSONFile    bool   `mapstructure:"json_file"`    // 落盘为 JSON 行；false 为 zap console 风格（便于人读）
	RedirectStd bool   `mapstructure:"redirect_std"` // 是否将标准库 log 包重定向到 zap（推荐 true）
}

// DatabaseConfig 数据库配置
type DatabaseConfig struct {
	Driver       string `mapstructure:"driver"`
	Host         string `mapstructure:"host"`
	Port         int    `mapstructure:"port"`
	User         string `mapstructure:"user"`
	Password     string `mapstructure:"password"`
	Database     string `mapstructure:"database"`
	SSLMode      string `mapstructure:"ssl_mode"`
	MaxIdleConns int    `mapstructure:"max_idle_conns"`
	MaxOpenConns int    `mapstructure:"max_open_conns"`
}

// SchedulerConfig 调度配置
type SchedulerConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	Preset  string `mapstructure:"preset"`
}

// PlatformConfig 平台配置
type PlatformConfig struct {
	Enabled bool           `mapstructure:"enabled"`
	Sources []SourceConfig `mapstructure:"sources"`
}

type SourceConfig struct {
	ID      string `mapstructure:"id"`
	Name    string `mapstructure:"name"`
	Enabled bool   `mapstructure:"enabled"`
}

// RSSConfig RSS 配置
type RSSConfig struct {
	Enabled         bool                  `mapstructure:"enabled"`
	Feeds           []FeedConfig          `mapstructure:"feeds"`
	FreshnessFilter FreshnessFilterConfig `mapstructure:"freshness_filter"`
}

type FeedConfig struct {
	ID         string `mapstructure:"id"`
	Name       string `mapstructure:"name"`
	URL        string `mapstructure:"url"`
	Enabled    bool   `mapstructure:"enabled"`
	MaxItems   int    `mapstructure:"max_items"`
	MaxAgeDays *int   `mapstructure:"max_age_days"`
}

type FreshnessFilterConfig struct {
	Enabled    bool `mapstructure:"enabled"`
	MaxAgeDays int  `mapstructure:"max_age_days"`
}

// ReportConfig 报告配置
type ReportConfig struct {
	Mode                string `mapstructure:"mode"`
	DisplayMode         string `mapstructure:"display_mode"`
	SortByPositionFirst bool   `mapstructure:"sort_by_position_first"`
	RankThreshold       int    `mapstructure:"rank_threshold"`
	MaxNewsPerKeyword   int    `mapstructure:"max_news_per_keyword"`
}

// FilterConfig 筛选配置
type FilterConfig struct {
	Method              string `mapstructure:"method"`
	Interests           string `mapstructure:"interests"`
	InterestsFile       string `mapstructure:"interests_file"` // 非空时从该路径读入全文覆盖 Interests；相对路径相对 config.yaml 所在目录
	PrioritySortEnabled bool   `mapstructure:"priority_sort_enabled"`
}

// AIConfig AI 配置
type AIConfig struct {
	Model          string   `mapstructure:"model"`
	APIKey         string   `mapstructure:"api_key"`
	APIBase        string   `mapstructure:"api_base"`
	Timeout        int      `mapstructure:"timeout"`
	Temperature    float64  `mapstructure:"temperature"`
	MaxTokens      int      `mapstructure:"max_tokens"`
	NumRetries     int      `mapstructure:"num_retries"`
	FallbackModels []string `mapstructure:"fallback_models"`
}

// AIAnalysisConfig AI 分析配置
type AIAnalysisConfig struct {
	Enabled             bool   `mapstructure:"enabled"`
	Language            string `mapstructure:"language"`
	PromptFile          string `mapstructure:"prompt_file"`
	Mode                string `mapstructure:"mode"`
	MaxNewsForAnalysis  int    `mapstructure:"max_news_for_analysis"`
	IncludeRSS          bool   `mapstructure:"include_rss"`
	IncludeStandalone   bool   `mapstructure:"include_standalone"`
	IncludeRankTimeline bool   `mapstructure:"include_rank_timeline"`
}

// AIFilterConfig AI 筛选配置（yaml 节名 ai_filter）
type AIFilterConfig struct {
	BatchSize   int `mapstructure:"batch_size"`   // 每批最多条数，防止一次喂太多条标题
	BatchInterval int `mapstructure:"batch_interval"` // 批次间间隔（毫秒），0=不睡；缓解限流与 CPU
	// MaxInputChars 单批 user 内容字符上限（按 Unicode 码点计，含兴趣全文+本批标题+模板尾）。
	// 0 表示不启用「按输入体量切分」，仅按 batch_size 切。
	MaxInputChars int `mapstructure:"max_input_chars"`
	// MaxOutputTokens 本任务单请求 max_tokens 覆盖；0 表示使用全局 ai.max_tokens（过滤建议单独设大，避免 JSON 被截断）
	MaxOutputTokens int `mapstructure:"max_output_tokens"`
	MinScore            float64 `mapstructure:"min_score"`
	ReclassifyThreshold float64 `mapstructure:"reclassify_threshold"`
	PromptFile          string  `mapstructure:"prompt_file"`
	ExtractPromptFile   string  `mapstructure:"extract_prompt_file"`
}

// AITranslationConfig AI 翻译配置
type AITranslationConfig struct {
	Enabled    bool   `mapstructure:"enabled"`
	Language   string `mapstructure:"language"`
	PromptFile string `mapstructure:"prompt_file"`
	Scope      struct {
		Hotlist    bool `mapstructure:"hotlist"`
		RSS        bool `mapstructure:"rss"`
		Standalone bool `mapstructure:"standalone"`
	} `mapstructure:"scope"`
}

// NotificationConfig 通知配置
type NotificationConfig struct {
	Enabled  bool          `mapstructure:"enabled"`
	Channels ChannelConfig `mapstructure:"channels"`
}

type ChannelConfig struct {
	Feishu         WebhookConfig  `mapstructure:"feishu"`
	DingTalk       WebhookConfig  `mapstructure:"dingtalk"`
	WeWork         WebhookConfig  `mapstructure:"wework"`
	Telegram       TelegramConfig `mapstructure:"telegram"`
	Email          EmailConfig    `mapstructure:"email"`
	Ntfy           NtfyConfig     `mapstructure:"ntfy"`
	Bark           BarkConfig     `mapstructure:"bark"`
	Slack          WebhookConfig  `mapstructure:"slack"`
	GenericWebhook WebhookConfig  `mapstructure:"generic_webhook"`
	// ServerChan 个人微信通知（Server 酱 sctapi，见 https://sct.ftqq.com）
	ServerChan ServerChanConfig `mapstructure:"serverchan"`
}

// ServerChanConfig Server 酱 SendKey，用于将推送转到用户已绑定的微信
type ServerChanConfig struct {
	SendKey      string `mapstructure:"sendkey"`
	// BatchEnabled 为 true 时：不随邮件即时推送，由定时任务按 slot 合并多段纯文本后推送（见 merge_segments）
	BatchEnabled  bool   `mapstructure:"batch_enabled"`
	SlotHours     string `mapstructure:"slot_hours"`       // 逗号分隔，如 8,11,14,17,20 约每 3 小时一次
	MaxPushesPerDay int  `mapstructure:"max_pushes_per_day"` // 每日 Server 酱最多条数，默认 5
	MergeSegments   int  `mapstructure:"merge_segments"`     // 每次合并「最近 N 段」小时摘要，默认 2
	// NotifyOnStartup 开启 batch 时：首次成功发邮件后补一条与邮件纯文（AI 过滤快报）相同的微信；非 batch 时一次 Send 已含微信
	NotifyOnStartup bool `mapstructure:"notify_on_startup"`
}

type WebhookConfig struct {
	WebhookURL      string `mapstructure:"webhook_url"`
	MsgType         string `mapstructure:"msg_type"`
	PayloadTemplate string `mapstructure:"payload_template"`
}

type TelegramConfig struct {
	BotToken string `mapstructure:"bot_token"`
	ChatID   string `mapstructure:"chat_id"`
}

type EmailConfig struct {
	From       string `mapstructure:"from"`
	Password   string `mapstructure:"password"`
	To         string `mapstructure:"to"`
	SMTPServer string `mapstructure:"smtp_server"`
	SMTPPort   string `mapstructure:"smtp_port"`
}

type NtfyConfig struct {
	ServerURL string `mapstructure:"server_url"`
	Topic     string `mapstructure:"topic"`
	Token     string `mapstructure:"token"`
}

type BarkConfig struct {
	URL string `mapstructure:"url"`
}

// StorageConfig 存储配置
type StorageConfig struct {
	Backend string       `mapstructure:"backend"`
	Formats FormatConfig `mapstructure:"formats"`
	Local   LocalConfig  `mapstructure:"local"`
	Remote  RemoteConfig `mapstructure:"remote"`
}

type FormatConfig struct {
	SQLite bool `mapstructure:"sqlite"`
	HTML   bool `mapstructure:"html"`
	TXT    bool `mapstructure:"txt"`
}

type LocalConfig struct {
	DataDir       string `mapstructure:"data_dir"`
	RetentionDays int    `mapstructure:"retention_days"`
}

type RemoteConfig struct {
	EndpointURL     string `mapstructure:"endpoint_url"`
	BucketName      string `mapstructure:"bucket_name"`
	AccessKeyID     string `mapstructure:"access_key_id"`
	SecretAccessKey string `mapstructure:"secret_access_key"`
	Region          string `mapstructure:"region"`
	RetentionDays   int    `mapstructure:"retention_days"`
}

// DailyExportConfig 每日新闻导出配置（推送到 ModelScope 数据集仓库）
type DailyExportConfig struct {
	Enabled             bool   `mapstructure:"enabled"`
	Cron                string `mapstructure:"cron"`             // 默认 "0 30 23 * * *"
	OutputDir           string `mapstructure:"output_dir"`       // 默认 "./data/daily_export"
	ModelScopeRepo      string `mapstructure:"modelscope_repo"`  // 如 "chensongpoixs/daily_news_corpus"
	ModelScopeToken     string `mapstructure:"modelscope_token"` // 访问令牌（也可用环境变量 MODEL_SCOPE_TOKEN）
	GitUser             string `mapstructure:"git_user"`
	GitEmail            string `mapstructure:"git_email"`
	FetchContent        bool   `mapstructure:"fetch_content"`         // 是否抓取原文正文内容
	MaxFetchConcurrency int    `mapstructure:"max_fetch_concurrency"` // 并发抓取数，默认 5
}

// AdvancedConfig 高级配置
type AdvancedConfig struct {
	Debug        bool `mapstructure:"debug"`
	VersionCheck struct {
		URL        string `mapstructure:"url"`
		MCPURL     string `mapstructure:"mcp_url"`
		ConfigsURL string `mapstructure:"configs_url"`
	} `mapstructure:"version_check_url"`
	Crawler               CrawlerConfig     `mapstructure:"crawler"`
	RSS                   RSSAdvancedConfig `mapstructure:"rss"`
	Weight                WeightConfig      `mapstructure:"weight"`
	MaxAccountsPerChannel int               `mapstructure:"max_accounts_per_channel"`
	BatchSize             BatchSizeConfig   `mapstructure:"batch_size"`
}

type CrawlerConfig struct {
	RequestInterval int    `mapstructure:"request_interval"`
	UseProxy        bool   `mapstructure:"use_proxy"`
	DefaultProxy    string `mapstructure:"default_proxy"`
}

type RSSAdvancedConfig struct {
	RequestInterval int    `mapstructure:"request_interval"`
	Timeout         int    `mapstructure:"timeout"`
	UseProxy        bool   `mapstructure:"use_proxy"`
	ProxyURL        string `mapstructure:"proxy_url"`
}

type WeightConfig struct {
	Rank      float64 `mapstructure:"rank"`
	Frequency float64 `mapstructure:"frequency"`
	Hotness   float64 `mapstructure:"hotness"`
}

type BatchSizeConfig struct {
	Default  int `mapstructure:"default"`
	DingTalk int `mapstructure:"dingtalk"`
	Feishu   int `mapstructure:"feishu"`
	Bark     int `mapstructure:"bark"`
	Slack    int `mapstructure:"slack"`
}

// Config 单例
var instance *Config
var v *viper.Viper
// configYAMLDir 当前使用的 config.yaml 所在目录，供 WebRoot 等相对路径解析
var configYAMLDir string

// Init 初始化配置
func Init(configPath string) error {
	v = viper.New()

	// 设置配置文件路径
	if configPath != "" {
		v.SetConfigFile(configPath)
	} else {
		// 约定：后端配置文件固定放在 backend-go/config/config.yaml
		v.SetConfigFile("./config/config.yaml")
	}

	// 读取环境变量
	v.SetEnvPrefix("TRENDRADAR")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// 读取配置文件
	if err := v.ReadInConfig(); err != nil {
		return err
	}
	if used := v.ConfigFileUsed(); used != "" {
		configYAMLDir = filepath.Dir(used)
	} else {
		configYAMLDir = ""
	}

	// 解码配置
	instance = &Config{}

	// 设置默认值
	setDefaults()

	if err := v.Unmarshal(instance); err != nil {
		return err
	}

	cfgPath := configPath
	if cfgPath == "" {
		cfgPath = v.ConfigFileUsed()
	}
	if err := applyInterestsFile(cfgPath, instance); err != nil {
		return err
	}

	return nil
}

// applyInterestsFile 若配置了 interests_file，则从与 config.yaml 同目录解析路径并读入，覆盖 filter.interests。
// 文件不存在或不可读时记录日志并保留 yaml 中的 interests 回退。
func applyInterestsFile(configYAMLPath string, c *Config) error {
	if c == nil {
		return nil
	}
	rel := strings.TrimSpace(c.Filter.InterestsFile)
	if rel == "" {
		return nil
	}
	if configYAMLPath == "" {
		log.Printf("filter: interests_file=%q set but config path unknown, skip file load", rel)
		return nil
	}
	base := filepath.Dir(configYAMLPath)
	full := filepath.Clean(filepath.Join(base, rel))
	b, err := os.ReadFile(full)
	if err != nil {
		log.Printf("filter: could not read interests_file %s: %v (using filter.interests from yaml if any)", full, err)
		return nil
	}
	text := strings.TrimSpace(string(b))
	if text == "" {
		log.Printf("filter: interests_file %s is empty, keeping filter.interests from yaml", full)
		return nil
	}
	c.Filter.Interests = text
	log.Printf("filter: loaded interests from %s (%d bytes)", full, len(text))
	return nil
}

// setDefaults 设置默认值
func setDefaults() {
	v.SetDefault("app.name", "趋势雷达")
	v.SetDefault("app.environment", "development")
	v.SetDefault("app.timezone", "Asia/Shanghai")
	v.SetDefault("app.version", "1.0.0")

	v.SetDefault("server.host", "0.0.0.0")
	v.SetDefault("server.port", 8080)
	v.SetDefault("server.mode", "debug")
	v.SetDefault("server.web_root", "")

	v.SetDefault("database.driver", "sqlite")
	v.SetDefault("database.database", "trendradar.db")
	v.SetDefault("database.max_idle_conns", 10)
	v.SetDefault("database.max_open_conns", 100)

	v.SetDefault("scheduler.enabled", true)
	v.SetDefault("scheduler.preset", "morning_evening")

	v.SetDefault("report.mode", "current")
	v.SetDefault("report.display_mode", "keyword")
	v.SetDefault("report.rank_threshold", 5)

	v.SetDefault("filter.method", "keyword")
	v.SetDefault("filter.interests", "")

	v.SetDefault("ai.model", "deepseek/deepseek-chat")
	v.SetDefault("ai.timeout", 120)
	v.SetDefault("ai.temperature", 1.0)
	v.SetDefault("ai.max_tokens", 5000)

	v.SetDefault("notification.enabled", true)
	v.SetDefault("notification.channels.serverchan.batch_enabled", false)
	v.SetDefault("notification.channels.serverchan.slot_hours", "8,11,14,17,20")
	v.SetDefault("notification.channels.serverchan.max_pushes_per_day", 5)
	v.SetDefault("notification.channels.serverchan.merge_segments", 2)
	v.SetDefault("notification.channels.serverchan.notify_on_startup", true)

	v.SetDefault("ai_filter.batch_size", 20)
	v.SetDefault("ai_filter.batch_interval", 0)
	v.SetDefault("ai_filter.max_input_chars", 12000)
	v.SetDefault("ai_filter.max_output_tokens", 0)

	v.SetDefault("storage.backend", "local")
	v.SetDefault("storage.formats.sqlite", true)
	v.SetDefault("storage.formats.html", true)
	v.SetDefault("storage.formats.txt", false)
	v.SetDefault("storage.local.data_dir", "./data")
	v.SetDefault("storage.local.retention_days", 30)

	v.SetDefault("daily_export.enabled", false)
	v.SetDefault("daily_export.cron", "0 30 23 * * *")
	v.SetDefault("daily_export.output_dir", "./data/daily_export")
	v.SetDefault("daily_export.modelscope_repo", "chensongpoixs/daily_news_corpus")
	v.SetDefault("daily_export.modelscope_token", "")
	v.SetDefault("daily_export.git_user", "chensongpoixs")
	v.SetDefault("daily_export.git_email", "chensongpoixs@example.com")
	v.SetDefault("daily_export.fetch_content", true)
	v.SetDefault("daily_export.max_fetch_concurrency", 5)

	v.SetDefault("logging.enabled", true)
	v.SetDefault("logging.level", "info")
	v.SetDefault("logging.file", "logs/trendradar.log")
	v.SetDefault("logging.max_size_mb", 100)
	v.SetDefault("logging.max_backups", 7)
	v.SetDefault("logging.max_age_days", 30)
	v.SetDefault("logging.compress", true)
	v.SetDefault("logging.console", true)
	v.SetDefault("logging.json_file", true)
	v.SetDefault("logging.redirect_std", true)
}

// Get 获取配置实例
func Get() *Config {
	return instance
}

// SanitizeNotificationForAPI 供 GET /config 等接口返回，避免敏感字段原文泄露
func SanitizeNotificationForAPI(n NotificationConfig) NotificationConfig {
	n2 := n
	if strings.TrimSpace(n2.Channels.ServerChan.SendKey) != "" {
		n2.Channels.ServerChan.SendKey = "(configured)"
	}
	return n2
}

// ResolveServerWebRoot 将 server.web_root 配置解析为绝对路径；未在配置中填写时返回空字符串（不检查路径是否存在）
func ResolveServerWebRoot() string {
	if instance == nil {
		return ""
	}
	raw := strings.TrimSpace(instance.Server.WebRoot)
	if raw == "" {
		return ""
	}
	if filepath.IsAbs(raw) {
		return filepath.Clean(raw)
	}
	if configYAMLDir != "" {
		return filepath.Clean(filepath.Join(configYAMLDir, raw))
	}
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return filepath.Clean(filepath.Join(wd, raw))
}

// GetViper 获取 viper 实例
func GetViper() *viper.Viper {
	return v
}

// GetEnvOrDefault 获取环境变量或默认值
func GetEnvOrDefault(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}
