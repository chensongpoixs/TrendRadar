package scheduler

import (
	"log"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/trendradar/backend-go/pkg/config"
)

// Scheduler 调度器
type Scheduler struct {
	cron      *cron.Cron
	enabled   bool
	preset    string
	mutex     sync.Mutex
	lastRun   map[string]time.Time
}

// NewScheduler 创建调度器实例
func NewScheduler() *Scheduler {
	cfg := config.Get().Scheduler

	s := &Scheduler{
		cron:    cron.New(cron.WithSeconds(), cron.WithLocation(time.FixedZone("CST", 8*3600))),
		enabled: cfg.Enabled,
		preset:  cfg.Preset,
		lastRun: make(map[string]time.Time),
	}

	return s
}

// Start 启动调度器
func (s *Scheduler) Start() error {
	if !s.enabled {
		log.Println("Scheduler is disabled")
		return nil
	}

	// 根据预设配置定时任务
	s.configureCronJobs()

	// 启动 cron
	s.cron.Start()
	log.Printf("Scheduler started with preset: %s", s.preset)

	return nil
}

// Stop 停止调度器
func (s *Scheduler) Stop() {
	if s.cron != nil {
		s.cron.Stop()
		log.Println("Scheduler stopped")
	}
}

// configureCronJobs 配置定时任务
func (s *Scheduler) configureCronJobs() {
	switch s.preset {
	case "always_on":
		// 每小时运行一次
		s.cron.AddFunc("33 * * * *", s.runCrawlTask)

	case "morning_evening":
		// 早上 8 点和晚上 8 点运行
		s.cron.AddFunc("33 8 * * *", s.runCrawlTask)
		s.cron.AddFunc("33 20 * * *", s.runCrawlTask)

	case "office_hours":
		// 工作日 9 点、13 点、17 点运行
		s.cron.AddFunc("33 9 * * 1-5", s.runCrawlTask)
		s.cron.AddFunc("33 13 * * 1-5", s.runCrawlTask)
		s.cron.AddFunc("33 17 * * 1-5", s.runCrawlTask)

	case "night_owl":
		// 下午 3 点和晚上 10 点运行
		s.cron.AddFunc("33 15 * * *", s.runCrawlTask)
		s.cron.AddFunc("33 22 * * *", s.runCrawlTask)

	default:
		// 默认每小时运行一次
		s.cron.AddFunc("33 * * * *", s.runCrawlTask)
	}
}

// runCrawlTask 运行抓取任务
func (s *Scheduler) runCrawlTask() {
	s.mutex.Lock()
	now := time.Now()
	taskKey := "crawl"

	// 检查是否刚运行过（防止重复执行）
	if lastRun, exists := s.lastRun[taskKey]; exists {
		if now.Sub(lastRun) < time.Hour {
			s.mutex.Unlock()
			log.Println("Task skipped: ran recently")
			return
		}
	}
	s.lastRun[taskKey] = now
	s.mutex.Unlock()

	log.Println("Running scheduled crawl task...")

	// TODO: 调用爬虫和推送逻辑
	// 1. 抓取平台数据
	// 2. 抓取 RSS 数据
	// 3. 保存数据
	// 4. 分析数据
	// 5. 推送通知
}

// RunNow 立即运行一次任务
func (s *Scheduler) RunNow() {
	s.runCrawlTask()
}

// IsEnabled 检查调度器是否启用
func (s *Scheduler) IsEnabled() bool {
	return s.enabled
}
