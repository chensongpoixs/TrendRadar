package scheduler

import (
	"fmt"
	"html"
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
		// 每天 0–23 点每小时整点运行
		addJob("0 0 * * * *")

	case "daytime_8_23":
		// 每天 8:00–23:00 每个整点各一次（共 16 次）
		addJob("0 0 8-23 * * *")

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
	results = ai.ApplyFocusFilter(results)

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

	filterMode := emailFilterStrategyLine(cfg)

	afterAICount := 0
	for _, items := range results {
		afterAICount += len(items)
	}

	emailResults, emailSkipped, dedupErr := storage.FilterNotYetEmailed(results)
	if dedupErr != nil {
		log.Printf("Email dedup query failed, sending without filter: %v", dedupErr)
		emailResults, emailSkipped = results, 0
	}
	mailCount := 0
	for _, items := range emailResults {
		mailCount += len(items)
	}
	dedupLine := ""
	if emailSkipped > 0 {
		dedupLine = fmt.Sprintf("\n邮件去重: 已排除历史已发送 %d 条", emailSkipped)
	}

	plainReport := fmt.Sprintf(
		"【趋势雷达】移动端行业快报\n\n[执行摘要]\n时间: %s\n平台: %d\n关注新闻(过滤后): %d\n本邮件新推送: %d%s\nRSS: %d\n失败平台: %v\n策略: %s\n\n[平台覆盖]\n%s\n\n[重点新闻TOP]\n%s",
		time.Now().Format(time.RFC3339),
		len(results),
		afterAICount,
		mailCount,
		dedupLine,
		rssTotal,
		failedIDs,
		filterMode,
		formatPlatformCoverage(emailResults, idToName),
		formatMobileNewsBrief(emailResults, idToName, crawlTime),
	)
	report := formatEmailHTML(emailResults, idToName, crawlTime, mailCount, rssTotal, failedIDs, filterMode, emailSkipped)

	if !cfg.Notification.Enabled {
		log.Printf("Notification disabled, skip sending email. Local data saved successfully.")
		return nil
	}
	if strings.TrimSpace(cfg.Notification.Channels.Email.To) == "" {
		log.Printf("Email recipient is empty, skip sending email. Local data saved successfully.")
		return nil
	}
	if mailCount == 0 {
		log.Printf("Email skipped: no new items after dedup (after AI: %d, excluded as already emailed: %d)", afterAICount, emailSkipped)
		return nil
	}
	dispatcher := notification.NewDispatcher()
	sendResult := dispatcher.Send("趋势雷达 每小时关注标题汇总", report)
	if ok, exists := sendResult["email"]; !exists || !ok {
		log.Printf("Email html send failed, fallback to plain report")
		sendResult = dispatcher.Send("趋势雷达 每小时关注标题汇总", plainReport)
	}
	if ok, exists := sendResult["email"]; !exists || !ok {
		return fmt.Errorf("email send failed: %v", sendResult)
	}
	if err := storage.RecordEmailSent(emailResults); err != nil {
		log.Printf("Record email fingerprints failed: %v", err)
	}
	log.Printf("Scheduled email sent successfully to %s (new: %d, dedup excluded: %d)", cfg.Notification.Channels.Email.To, mailCount, emailSkipped)
	return nil
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

func formatMobileNewsBrief(results map[string][]model.NewsItem, idToName map[string]string, fallbackTime time.Time) string {
	if len(results) == 0 {
		return "无重点新闻"
	}

	const maxPerPlatform = 5
	var b strings.Builder
	for platformID, items := range results {
		if len(items) == 0 {
			continue
		}
		platformName := idToName[platformID]
		if strings.TrimSpace(platformName) == "" {
			platformName = platformID
		}
		b.WriteString(fmt.Sprintf("\n%s\n", platformName))

		limit := len(items)
		if limit > maxPerPlatform {
			limit = maxPerPlatform
		}
		for i := 0; i < limit; i++ {
			item := items[i]
			title := strings.TrimSpace(item.Title)
			if len([]rune(title)) > 46 {
				runes := []rune(title)
				title = string(runes[:46]) + "..."
			}
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
			b.WriteString(fmt.Sprintf("%d) %s\n", i+1, title))
			b.WriteString(fmt.Sprintf("   %s | %s\n", itemTime.Format("15:04"), link))
		}
	}
	return strings.TrimSpace(b.String())
}

func formatEmailHTML(results map[string][]model.NewsItem, idToName map[string]string, fallbackTime time.Time, totalCount, rssTotal int, failedIDs []string, filterMode string, dedupSkipped int) string {
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width, initial-scale=1.0">`)
	b.WriteString(`<style>body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,"PingFang SC","Microsoft YaHei",sans-serif;background:#f5f7fb;margin:0;padding:12px;color:#111827}.wrap{max-width:760px;margin:0 auto;background:#fff;border-radius:12px;overflow:hidden;border:1px solid #e5e7eb}.hero{background:linear-gradient(135deg,#4f46e5,#7c3aed);color:#fff;padding:20px}.title{font-size:24px;font-weight:700}.meta{display:flex;gap:24px;flex-wrap:wrap;margin-top:14px;font-size:13px}.section{padding:14px 16px;border-top:1px solid #eef2f7}.h{font-size:15px;font-weight:700;margin:0 0 10px}.platform{margin:10px 0 4px;font-weight:700;color:#374151}.item{padding:10px 0;border-top:1px dashed #e5e7eb}.item:first-child{border-top:none}.t{font-size:14px;line-height:1.5}.m{font-size:12px;color:#6b7280;margin-top:4px}.a{color:#2563eb;text-decoration:none;word-break:break-all}.foot{padding:14px 16px;color:#6b7280;font-size:12px;text-align:center}</style></head><body><div class="wrap">`)
	b.WriteString(`<div class="hero"><div class="title">热点新闻分析</div>`)
	b.WriteString(`<div class="meta">`)
	b.WriteString(fmt.Sprintf(`<div><div>本批新内容</div><strong>%d 条</strong></div>`, totalCount))
	b.WriteString(fmt.Sprintf(`<div><div>RSS</div><strong>%d 条</strong></div>`, rssTotal))
	b.WriteString(fmt.Sprintf(`<div><div>生成时间</div><strong>%s</strong></div>`, html.EscapeString(time.Now().Format("01-02 15:04"))))
	b.WriteString(`</div></div>`)
	b.WriteString(`<div class="section"><p style="margin:0;font-size:13px;color:#374151">过滤策略：` + html.EscapeString(filterMode) + `</p>`)
	if dedupSkipped > 0 {
		b.WriteString(`<p style="margin:6px 0 0;font-size:12px;color:#1d4ed8">本小时已去重，未重复发送历史已推送 ` + fmt.Sprintf("%d", dedupSkipped) + ` 条</p>`)
	}
	if len(failedIDs) > 0 {
		b.WriteString(`<p style="margin:6px 0 0;font-size:12px;color:#b91c1c">失败平台：` + html.EscapeString(strings.Join(failedIDs, ",")) + `</p>`)
	}
	b.WriteString(`</div><div class="section"><h3 class="h">重点新闻</h3>`)

	const maxPerPlatform = 8
	for platformID, items := range results {
		if len(items) == 0 {
			continue
		}
		name := idToName[platformID]
		if strings.TrimSpace(name) == "" {
			name = platformID
		}
		b.WriteString(`<div class="platform">` + html.EscapeString(name) + `（` + fmt.Sprintf("%d", len(items)) + `条）</div>`)
		limit := len(items)
		if limit > maxPerPlatform {
			limit = maxPerPlatform
		}
		for i := 0; i < limit; i++ {
			item := items[i]
			link := strings.TrimSpace(item.URL)
			if link == "" {
				link = strings.TrimSpace(item.MobileURL)
			}
			if link == "" {
				link = "#"
			}
			itemTime := item.CrawlTime
			if itemTime.IsZero() {
				itemTime = fallbackTime
			}
			b.WriteString(`<div class="item"><div class="t">` + fmt.Sprintf("%d. ", i+1) + html.EscapeString(strings.TrimSpace(item.Title)) + `</div>`)
			b.WriteString(`<div class="m">` + html.EscapeString(itemTime.Format("15:04")) + ` · <a class="a" href="` + html.EscapeString(link) + `" target="_blank">查看原文</a></div></div>`)
		}
	}
	b.WriteString(`</div><div class="foot">由 趋势雷达 生成 · GitHub 开源项目</div></div></body></html>`)
	return b.String()
}

// emailFilterStrategyLine 供邮件「过滤策略」一行展示：不内嵌整份兴趣全文，避免长文刷屏。
func emailFilterStrategyLine(cfg *config.Config) string {
	if strings.ToLower(cfg.Filter.Method) != "ai" || strings.TrimSpace(cfg.Filter.Interests) == "" {
		return "AI 关注标题过滤: 未启用"
	}
	if f := strings.TrimSpace(cfg.Filter.InterestsFile); f != "" {
		return "AI 关注标题过滤: 已启用（兴趣配置: " + f + "）"
	}
	line := firstNonCommentLineForEmail(cfg.Filter.Interests)
	if line == "" {
		return "AI 关注标题过滤: 已启用"
	}
	runes := []rune(line)
	if len(runes) > 100 {
		line = string(runes[:100]) + "…"
	}
	return "AI 关注标题过滤: 已启用（兴趣摘要: " + line + "）"
}

func firstNonCommentLineForEmail(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		return line
	}
	return ""
}
