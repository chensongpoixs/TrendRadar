package scheduler

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/trendradar/backend-go/internal/ai"
	"github.com/trendradar/backend-go/internal/crawler"
	"github.com/trendradar/backend-go/internal/notification"
	"github.com/trendradar/backend-go/internal/storage"
	"github.com/trendradar/backend-go/pkg/config"
	"github.com/trendradar/backend-go/pkg/model"
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
	addJob := func(spec string) {
		if _, err := s.cron.AddFunc(spec, s.runCrawlTask); err != nil {
			log.Printf("Failed to add cron job (%s): %v", spec, err)
		}
	}

	switch s.preset {
	case "always_on":
		// 每小时整点运行（带秒字段）
		addJob("0 0 * * * *")

	case "morning_evening":
		// 早上 8 点和晚上 8 点运行
		addJob("0 0 8 * * *")
		addJob("0 0 20 * * *")

	case "office_hours":
		// 工作日 9 点、13 点、17 点运行
		addJob("0 0 9 * * 1-5")
		addJob("0 0 13 * * 1-5")
		addJob("0 0 17 * * 1-5")

	case "night_owl":
		// 下午 3 点和晚上 10 点运行
		addJob("0 0 15 * * *")
		addJob("0 0 22 * * *")

	default:
		// 默认每小时运行一次
		addJob("0 0 * * * *")
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
	if err := runCrawlAnalyzeAndNotify(); err != nil {
		log.Printf("Scheduled task failed: %v", err)
	}
}

// RunNow 立即运行一次任务
func (s *Scheduler) RunNow() {
	s.runCrawlTask()
}

// IsEnabled 检查调度器是否启用
func (s *Scheduler) IsEnabled() bool {
	return s.enabled
}

func runCrawlAnalyzeAndNotify() error {
	cfg := config.Get()
	if cfg == nil {
		return fmt.Errorf("config not initialized")
	}

	platformCrawler := crawler.NewPlatformCrawler()
	results, idToName, failedIDs, err := platformCrawler.CrawlAll()
	if err != nil {
		return err
	}
	results = applyAIFocusFilter(results)

	// 本地持久化，方便前端查询历史
	newsStorage := storage.NewNewsStorage()
	crawlTime := time.Now()
	for platformID, items := range results {
		if err := newsStorage.SaveNewsData(platformID, items, crawlTime); err != nil {
			log.Printf("Failed to save scheduled platform data for %s: %v", platformID, err)
		}
	}

	// 抓取并持久化 RSS（可选）
	rssTotal := 0
	if cfg.RSS.Enabled {
		rssCrawler := crawler.NewRSSCrawler()
		rssResults, _, rssFailedIDs, rssErr := rssCrawler.FetchAll()
		if rssErr != nil {
			log.Printf("Scheduled RSS crawl failed: %v", rssErr)
		} else {
			for feedID, items := range rssResults {
				if err := newsStorage.SaveRSSData(feedID, items, crawlTime); err != nil {
					log.Printf("Failed to save scheduled RSS data for %s: %v", feedID, err)
				}
				rssTotal += len(items)
			}
			if len(rssFailedIDs) > 0 {
				log.Printf("Scheduled RSS failed ids: %v", rssFailedIDs)
			}
		}
	}

	filterMode := "AI 关注标题过滤: 未启用"
	if strings.ToLower(cfg.Filter.Method) == "ai" && strings.TrimSpace(cfg.Filter.Interests) != "" {
		filterMode = fmt.Sprintf("AI 关注标题过滤: 已启用（兴趣词：%s）", strings.TrimSpace(cfg.Filter.Interests))
	}

	totalCount := 0
	for _, items := range results {
		totalCount += len(items)
	}
	report := fmt.Sprintf(
		"趋势雷达 | 每小时热点情报简报\n\n一、执行摘要\n- 抓取时间：%s\n- 覆盖平台：%d\n- 关注新闻：%d\n- RSS 条目：%d\n- 抓取失败：%v\n- 过滤策略：%s\n\n二、平台覆盖\n%s\n\n三、重点新闻明细\n%s",
		time.Now().Format(time.RFC3339),
		len(results),
		totalCount,
		rssTotal,
		failedIDs,
		filterMode,
		formatPlatformCoverage(results, idToName),
		formatNewsDetails(results, idToName, crawlTime),
	)

	if !cfg.Notification.Enabled {
		log.Printf("Notification disabled, skip sending email. Local data saved successfully.")
		return nil
	}
	if strings.TrimSpace(cfg.Notification.Channels.Email.To) == "" {
		log.Printf("Email recipient is empty, skip sending email. Local data saved successfully.")
		return nil
	}
	dispatcher := notification.NewDispatcher()
	sendResult := dispatcher.Send("趋势雷达 每小时关注标题汇总", report)
	if ok, exists := sendResult["email"]; !exists || !ok {
		return fmt.Errorf("email send failed: %v", sendResult)
	}
	log.Printf("Scheduled email sent successfully to %s", cfg.Notification.Channels.Email.To)
	return nil
}

func applyAIFocusFilter(results map[string][]model.NewsItem) map[string][]model.NewsItem {
	cfg := config.Get()
	if cfg == nil || strings.ToLower(cfg.Filter.Method) != "ai" || strings.TrimSpace(cfg.Filter.Interests) == "" {
		return results
	}

	flat := make([]ai.NewsItem, 0)
	type itemRef struct {
		platformID string
		item       model.NewsItem
	}
	refs := make([]itemRef, 0)
	for platformID, items := range results {
		for _, item := range items {
			flat = append(flat, ai.NewsItem{
				Title:  item.Title,
				Rank:   item.Rank,
				Source: platformID,
			})
			refs = append(refs, itemRef{platformID: platformID, item: item})
		}
	}
	if len(flat) == 0 {
		return results
	}

	filter := ai.NewFilter(cfg.Filter.Interests, cfg.AIFilter.MinScore, cfg.AIFilter.BatchSize)
	filterResults, err := filter.FilterNews(flat)
	if err != nil {
		log.Printf("AI focus filter failed in scheduler, fallback to original data: %v", err)
		return results
	}
	minScore := cfg.AIFilter.MinScore
	if minScore <= 0 {
		minScore = 0.7
	}
	filterResults = ai.GetFilteredItems(filterResults, minScore)

	allowed := make(map[string]bool)
	for _, r := range filterResults {
		aiItem, ok := r.Item.(ai.NewsItem)
		if !ok {
			continue
		}
		allowed[aiItem.Source+"::"+aiItem.Title] = true
	}

	kept := make(map[string][]model.NewsItem)
	for _, ref := range refs {
		key := ref.platformID + "::" + ref.item.Title
		if allowed[key] {
			kept[ref.platformID] = append(kept[ref.platformID], ref.item)
		}
	}
	log.Printf("Scheduled AI focus filter applied with min_score=%.2f: %d -> %d", minScore, len(refs), len(filterResults))
	return kept
}

func formatNewsDetails(results map[string][]model.NewsItem, idToName map[string]string, fallbackTime time.Time) string {
	if len(results) == 0 {
		return "新闻明细：无"
	}

	var b strings.Builder
	b.WriteString("新闻明细（标题 / 链接 / 抓取时间）：\n")

	for platformID, items := range results {
		if len(items) == 0 {
			continue
		}
		platformName := idToName[platformID]
		if strings.TrimSpace(platformName) == "" {
			platformName = platformID
		}
		b.WriteString(fmt.Sprintf("\n【%s】\n", platformName))

		for i, item := range items {
			link := strings.TrimSpace(item.URL)
			if link == "" {
				link = strings.TrimSpace(item.MobileURL)
			}
			if link == "" {
				link = "无链接"
			}

			itemTime := item.CrawlTime
			if itemTime.IsZero() {
				itemTime = fallbackTime
			}

			b.WriteString(fmt.Sprintf("%d. %s\n", i+1, strings.TrimSpace(item.Title)))
			b.WriteString(fmt.Sprintf("   链接: %s\n", link))
			b.WriteString(fmt.Sprintf("   时间: %s\n", itemTime.Format("2006-01-02 15:04:05")))
		}
	}

	return b.String()
}

func formatPlatformCoverage(results map[string][]model.NewsItem, idToName map[string]string) string {
	if len(results) == 0 {
		return "- 无"
	}
	var b strings.Builder
	for platformID, items := range results {
		platformName := idToName[platformID]
		if strings.TrimSpace(platformName) == "" {
			platformName = platformID
		}
		b.WriteString(fmt.Sprintf("- %s：%d 条\n", platformName, len(items)))
	}
	return strings.TrimSpace(b.String())
}
