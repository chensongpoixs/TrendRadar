package core

import (
	"fmt"
	"log"
	"time"

	"github.com/trendradar/backend-go/pkg/config"
	"github.com/trendradar/backend-go/pkg/model"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var DB *gorm.DB

// InitDatabase 初始化数据库连接
func InitDatabase() error {
	cfg := config.Get().Database
	var dsn string
	var dialector gorm.Dialector

	switch cfg.Driver {
	case "postgres":
		dsn = fmt.Sprintf(
			"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
			cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.Database, cfg.SSLMode,
		)
		dialector = postgres.Open(dsn)
	case "sqlite":
		fallthrough
	default:
		dsn = cfg.Database
		if dsn == "" {
			dsn = ":memory:"
		}
		dialector = sqlite.Open(dsn)
	}

	var err error
	DB, err = gorm.Open(dialector, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Info),
	})
	if err != nil {
		return fmt.Errorf("failed to connect database: %w", err)
	}

	// 配置数据库连接池
	if cfg.Driver == "postgres" {
		db, err := DB.DB()
		if err != nil {
			return err
		}
		db.SetMaxIdleConns(cfg.MaxIdleConns)
		db.SetMaxOpenConns(cfg.MaxOpenConns)
		db.SetConnMaxLifetime(time.Hour)
	}

	// 自动迁移数据库表
	if err := migrateDatabase(); err != nil {
		return fmt.Errorf("failed to migrate database: %w", err)
	}

	log.Println("Database connected successfully")
	return nil
}

// migrateDatabase 数据库迁移
func migrateDatabase() error {
	return DB.AutoMigrate(
		&model.NewsItem{},
		&model.RSSItem{},
		&model.Platform{},
		&model.RSSFeed{},
		&model.CrawlRecord{},
		&model.RankHistory{},
		&model.EmailSentFingerprint{},
	)
}

// GetDB 获取数据库实例
func GetDB() *gorm.DB {
	return DB
}
