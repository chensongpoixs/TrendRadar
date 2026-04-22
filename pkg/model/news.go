package model

import (
	"time"

	"gorm.io/gorm"
)

// NewsItem 新闻项
type NewsItem struct {
	ID           uint           `gorm:"primaryKey" json:"id"`
	Title        string         `gorm:"type:text;not null" json:"title"`
	SourceID     string         `gorm:"type:varchar(50);index" json:"source_id"`
	SourceName   string         `gorm:"type:varchar(100)" json:"source_name"`
	Rank         int            `gorm:"default:0" json:"rank"`
	URL          string         `gorm:"type:text;index" json:"url"`
	MobileURL    string         `gorm:"type:text" json:"mobile_url"`
	CrawlTime    time.Time      `gorm:"index" json:"crawl_time"`
	FirstTime    time.Time      `gorm:"index" json:"first_time"`
	LastTime     time.Time      `json:"last_time"`
	Count        int            `gorm:"default:1" json:"count"`
	Ranks        string         `gorm:"type:text" json:"ranks"` // JSON 数组字符串
	RankTimeline string         `gorm:"type:text" json:"rank_timeline"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	DeletedAt    gorm.DeletedAt `gorm:"index" json:"deleted_at"`
}

// TableName 指定表名
func (NewsItem) TableName() string {
	return "news_items"
}

// RSSItem RSS 订阅项
type RSSItem struct {
	ID          uint    `gorm:"primaryKey" json:"id"`
	Title       string  `gorm:"type:text;not null" json:"title"`
	FeedID      string  `gorm:"type:varchar(50);index" json:"feed_id"`
	FeedName    string  `gorm:"type:varchar(100)" json:"feed_name"`
	URL         string  `gorm:"type:text;uniqueIndex:" json:"url"`
	PublishedAt string  `gorm:"type:text" json:"published_at"`
	Summary     string  `gorm:"type:text" json:"summary"`
	Author      string  `gorm:"type:varchar(200)" json:"author"`
	CrawlTime   time.Time `gorm:"index" json:"crawl_time"`
	FirstTime   time.Time `json:"first_time"`
	LastTime    time.Time `json:"last_time"`
	Count       int     `gorm:"default:1" json:"count"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (RSSItem) TableName() string {
	return "rss_items"
}

// Platform 平台配置
type Platform struct {
	ID   string `gorm:"primaryKey;type:varchar(50)" json:"id"`
	Name string `gorm:"type:varchar(100)" json:"name"`
	Enabled bool `gorm:"default:true" json:"enabled"`
}

func (Platform) TableName() string {
	return "platforms"
}

// RSSFeed RSS 源配置
type RSSFeed struct {
	ID          string `gorm:"primaryKey;type:varchar(50)" json:"id"`
	Name        string `gorm:"type:varchar(100)" json:"name"`
	URL         string `gorm:"type:text" json:"url"`
	Enabled     bool   `gorm:"default:true" json:"enabled"`
	MaxItems    int    `gorm:"default:0" json:"max_items"`
	MaxAgeDays  int    `gorm:"default:0" json:"max_age_days"`
}

func (RSSFeed) TableName() string {
	return "rss_feeds"
}

// CrawlRecord 抓取记录
type CrawlRecord struct {
	ID          uint `gorm:"primaryKey" json:"id"`
	CrawlTime   time.Time `gorm:"uniqueIndex;index" json:"crawl_time"`
	TotalItems  int `json:"total_items"`
	CreatedAt   time.Time `json:"created_at"`
}

func (CrawlRecord) TableName() string {
	return "crawl_records"
}

// RankHistory 排名历史
type RankHistory struct {
	ID        uint `gorm:"primaryKey" json:"id"`
	NewsItemID uint `gorm:"index" json:"news_item_id"`
	Rank      int `json:"rank"`
	CrawlTime time.Time `gorm:"index" json:"crawl_time"`
	CreatedAt time.Time `json:"created_at"`
}

func (RankHistory) TableName() string {
	return "rank_history"
}

// Topic 话题
type Topic struct {
	Word      string `json:"word"`
	Count     int    `json:"count"`
	Position  int    `json:"position"`
	Titles    []TitleItem `json:"titles"`
	Percentage float64 `json:"percentage,omitempty"`
}

// TitleItem 标题项
type TitleItem struct {
	Title         string `json:"title"`
	SourceName    string `json:"source_name"`
	URL           string `json:"url"`
	MobileURL     string `json:"mobile_url"`
	Ranks         string `json:"ranks"`
	RankThreshold int    `json:"rank_threshold"`
	Count         int    `json:"count"`
	IsNew         bool   `json:"is_new"`
	TimeDisplay   string `json:"time_display"`
}

// SearchOptions 搜索选项
type SearchOptions struct {
	Query       string
	SearchMode  string // keyword, fuzzy, entity
	DateStart   string
	DateEnd     string
	Platforms   []string
	Limit       int
	SortBy      string // relevance, weight, date
	Threshold   float64
}
