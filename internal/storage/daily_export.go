package storage

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/trendradar/backend-go/internal/ai"
	"github.com/trendradar/backend-go/internal/core"
	"github.com/trendradar/backend-go/pkg/config"
	"github.com/trendradar/backend-go/pkg/model"
	applog "github.com/trendradar/backend-go/pkg/logger"
	"go.uber.org/zap"
)

const maxFetchTextRunes = 12000 // 单篇文章抓取最大字符数，超出截断

// enrichedNews 携带正文抓取结果的快照条目
type enrichedNews struct {
	model.HotlistSnapshot
	Content  string // 抓取到的正文纯文本
	Fetched  bool   // 是否成功抓取
	FetchErr string // 抓取错误信息
}

// RunDailyExport 对指定 date（格式 2006-01-02，即 YYYY-MM-DD）执行每日导出并推送。
// 若 date 为空则使用当天（按应用时区）。
func RunDailyExport(date string) error {
	cfg := config.Get()
	if cfg == nil || !cfg.DailyExport.Enabled {
		return fmt.Errorf("daily_export not enabled in config")
	}
	tz := appTimeLocation()
	if date == "" {
		date = time.Now().In(tz).Format("2006-01-02")
	}
	ymd := strings.ReplaceAll(date, "-", "")
	year := ymd[0:4]
	month := ymd[4:6]

	l := applog.WithComponent("daily_export").With(zap.String("date", date), zap.String("ymd", ymd))
	l.Info("[Step 1/5] Daily export started ==========")

	// 1. 查询当日去重后的全量热榜快照
	platforms, err := QueryDailySnapshots(date)
	if err != nil {
		l.Error("[Step 1/5] Query snapshots failed", zap.Error(err))
		return fmt.Errorf("query snapshots: %w", err)
	}
	if len(platforms) == 0 {
		l.Warn("[Step 1/5] No hotlist snapshots for this date, skip export")
		return nil
	}
	totalNews := 0
	for pid, items := range platforms {
		totalNews += len(items)
		l.Info("  Platform snapshot stats", zap.String("platform", pid), zap.Int("count", len(items)))
	}
	l.Info(fmt.Sprintf("[Step 1/5] Snapshots loaded: %d platforms, %d deduplicated news items", len(platforms), totalNews))

	// 2. 并发抓取所有文章正文（手动触发时可跳过以加速）
	fetchContent := cfg.DailyExport.FetchContent
	concurrency := cfg.DailyExport.MaxFetchConcurrency
	if concurrency <= 0 {
		concurrency = 5
	}
	l.Info(fmt.Sprintf("[Step 2/5] Fetch article content: enabled=%v, concurrency=%d", fetchContent, concurrency))
	enriched := enrichAllPlatforms(platforms, fetchContent, concurrency, l)
	l.Info("[Step 2/5] Article content fetch completed")

	// 3. 尝试获取当日行业 AI 研报
	l.Info("[Step 3/5] Fetching daily industry AI report...")
	var reportContent string
	ns := NewNewsStorage()
	report, err := ns.GetDayIndustryReport(date)
	if err != nil {
		l.Warn("[Step 3/5] Failed to get industry report", zap.Error(err))
	}
	if report != nil && strings.TrimSpace(report.Content) != "" {
		reportContent = report.Content
		l.Info(fmt.Sprintf("[Step 3/5] Industry report loaded, length=%d chars", len(reportContent)))
	} else {
		l.Info("[Step 3/5] No industry report for this date, skipped")
	}

	// 4. 生成目录结构
	outputDir := cfg.DailyExport.OutputDir
	if outputDir == "" {
		outputDir = "./data/daily_export"
	}
	l.Info(fmt.Sprintf("[Step 4/5] Build export directory: output_dir=%s", outputDir))
	exportPath, err := buildExportDir(outputDir, ymd, enriched, reportContent)
	if err != nil {
		l.Error("[Step 4/5] Build export directory failed", zap.Error(err))
		return fmt.Errorf("build export dir: %w", err)
	}
	l.Info(fmt.Sprintf("[Step 4/5] Export directory built: path=%s", exportPath))

	// 5. 推送到 ModelScope
	repo := cfg.DailyExport.ModelScopeRepo
	token := strings.TrimSpace(cfg.DailyExport.ModelScopeToken)
	if token == "" {
		token = strings.TrimSpace(os.Getenv("MODEL_SCOPE_TOKEN"))
	}
	l.Info(fmt.Sprintf("[Step 5/5] Push to ModelScope: repo=%s, token_set=%v", repo, token != ""))
	if repo == "" {
		l.Warn("[Step 5/5] modelscope_repo not configured, skip push")
		return nil
	}
	if token == "" {
		l.Warn("[Step 5/5] modelscope_token not configured, skip push")
		return nil
	}

	gitUser := cfg.DailyExport.GitUser
	if gitUser == "" {
		gitUser = "chensongpoixs"
	}
	gitEmail := cfg.DailyExport.GitEmail
	if gitEmail == "" {
		gitEmail = "chensongpoixs@example.com"
	}

	if err := pushToModelScope(exportPath, year, month, ymd, repo, token, gitUser, gitEmail); err != nil {
		l.Error("[Step 5/5] Push to ModelScope failed", zap.Error(err))
		l.Info(fmt.Sprintf("[Step 5/5] Local export still available: path=%s", exportPath))
		return fmt.Errorf("push to modelscope: %w", err)
	}

	l.Info(fmt.Sprintf("[Step 5/5] Push to ModelScope succeeded! Daily export complete! path=%s", exportPath))
	return nil
}

// enrichAllPlatforms 对所有平台的新闻执行并发正文抓取
func enrichAllPlatforms(platforms map[string][]model.HotlistSnapshot, fetchContent bool, concurrency int, l *zap.Logger) map[string][]enrichedNews {
	result := make(map[string][]enrichedNews, len(platforms))

	for pid, items := range platforms {
		result[pid] = make([]enrichedNews, len(items))
		// 先初始化，未抓取时也有基础信息
		for i, item := range items {
			result[pid][i] = enrichedNews{HotlistSnapshot: item}
		}
	}

	if !fetchContent {
		l.Info("content fetch disabled by config")
		return result
	}

	// 展平所有条目，记录位置
	type slot struct {
		platformID string
		index      int
		url        string
		mobileURL  string
	}
	var slots []slot
	skippedSearch := 0
	for pid, items := range platforms {
		for i, item := range items {
			rawURL := strings.TrimSpace(item.URL)
			mURL := strings.TrimSpace(item.MobileURL)
			if rawURL == "" && mURL == "" {
				continue
			}
			if rawURL != "" {
				if reason, skip := checkUnfetchableURL(rawURL); skip {
					result[pid][i].FetchErr = reason
					skippedSearch++
					continue
				}
			}
			slots = append(slots, slot{platformID: pid, index: i, url: rawURL, mobileURL: mURL})
		}
	}
	if skippedSearch > 0 {
		l.Info("skipped unfetchable urls", zap.Int("count", skippedSearch))
	}

	if len(slots) == 0 {
		l.Info("no urls to fetch")
		return result
	}

	l.Info("starting concurrent content fetch", zap.Int("total", len(slots)), zap.Int("concurrency", concurrency))

	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	ctx := context.Background()
	fetched := 0
	var mu sync.Mutex

	for _, s := range slots {
		wg.Add(1)
		go func(sl slot) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			text, err := ai.FetchArticleWithFallback(ctx, sl.url, sl.mobileURL)
			r := &result[sl.platformID][sl.index]
			if err != nil {
				r.FetchErr = err.Error()
				return
			}
			text = strings.TrimSpace(text)
			if text == "" {
				r.FetchErr = "empty body"
				return
			}
			// 截断
			if utf8.RuneCountInString(text) > maxFetchTextRunes {
				text = string([]rune(text)[:maxFetchTextRunes])
			}
			r.Content = text
			r.Fetched = true
			mu.Lock()
			fetched++
			mu.Unlock()
		}(s)
	}
	wg.Wait()

	l.Info("content fetch done", zap.Int("fetched", fetched), zap.Int("total", len(slots)))
	return result
}

// checkUnfetchableURL 检测不可抓取的 URL 类型（如搜索引擎跳转链接），
// 返回提示原因与是否应跳过抓取。
func checkUnfetchableURL(rawURL string) (reason string, skip bool) {
	// 百度搜索跳转链接（/s?wd=...），本质是搜索关键词，非具体文章
	if strings.Contains(rawURL, "baidu.com/s?wd=") || strings.Contains(rawURL, "baidu.com/s?") {
		return "百度热搜条目为搜索关键词链接，非具体文章 URL，无法抓取正文", true
	}
	return "", false
}

// QueryDailySnapshots 查询当日热榜快照，按平台分组并做 URL 去重（取 rank 最优）。
func QueryDailySnapshots(date string) (map[string][]model.HotlistSnapshot, error) {
	db := core.GetDB()
	var raw []model.HotlistSnapshot
	if err := db.Where("date_local = ?", date).Order("rank ASC, captured_at DESC").Find(&raw).Error; err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	out := make(map[string][]model.HotlistSnapshot)
	for _, r := range raw {
		key := r.SourceID + "\x00" + r.URL
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out[r.SourceID] = append(out[r.SourceID], r)
	}
	for k := range out {
		sort.Slice(out[k], func(i, j int) bool {
			ri, rj := out[k][i].Rank, out[k][j].Rank
			if ri == 0 && rj == 0 {
				return out[k][i].Title < out[k][j].Title
			}
			if ri == 0 {
				return false
			}
			if rj == 0 {
				return true
			}
			return ri < rj
		})
	}
	return out, nil
}

// buildExportDir 生成 年份/月份/YYYYMMDD/平台/新闻标题.md 的目录结构，返回导出根路径。
func buildExportDir(baseDir, ymd string, platforms map[string][]enrichedNews, reportContent string) (string, error) {
	year := ymd[0:4]
	month := ymd[4:6]
	root := filepath.Join(baseDir, year, month, ymd)
	if err := os.RemoveAll(root); err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0750); err != nil {
		return "", err
	}

	for platformID, items := range platforms {
		platformDir := filepath.Join(root, sanitizeFilename(platformID))
		if err := os.MkdirAll(platformDir, 0750); err != nil {
			return "", err
		}
		for _, item := range items {
			fname := sanitizeFilename(item.Title) + ".md"
			fpath := filepath.Join(platformDir, fname)
			fpath = uniqueFilePath(fpath)
			content := buildNewsMarkdown(item)
			if err := os.WriteFile(fpath, []byte(content), 0640); err != nil {
				return "", fmt.Errorf("write %s: %w", fpath, err)
			}
		}
	}

	readmeContent := buildReadmeMD(ymd, platforms, reportContent)
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte(readmeContent), 0640); err != nil {
		return "", err
	}

	return root, nil
}

func buildReadmeMD(ymd string, platforms map[string][]enrichedNews, reportContent string) string {
	var b strings.Builder
	dateDisplay := ymd
	if len(ymd) == 8 {
		dateDisplay = ymd[0:4] + "-" + ymd[4:6] + "-" + ymd[6:8]
	}
	b.WriteString(fmt.Sprintf("# 趋势雷达 · 每日新闻语料 (%s)\n\n", dateDisplay))
	b.WriteString("> 由 TrendRadar 自动生成，每日 23:30 更新。\n\n")

	total := 0
	fetched := 0
	platformNames := make([]string, 0, len(platforms))
	for pid, items := range platforms {
		platformNames = append(platformNames, pid)
		total += len(items)
		for _, it := range items {
			if it.Fetched {
				fetched++
			}
		}
	}
	sort.Strings(platformNames)

	b.WriteString("## 数据集概览\n\n")
	b.WriteString(fmt.Sprintf("- **日期**: %s\n", dateDisplay))
	b.WriteString(fmt.Sprintf("- **平台数**: %d\n", len(platforms)))
	b.WriteString(fmt.Sprintf("- **新闻总数**: %d\n", total))
	b.WriteString(fmt.Sprintf("- **成功抓取正文**: %d 篇\n\n", fetched))

	b.WriteString("## 平台分布\n\n")
	b.WriteString("| 平台 | 新闻数 |\n")
	b.WriteString("|------|-------|\n")
	for _, pid := range platformNames {
		b.WriteString(fmt.Sprintf("| %s | %d |\n", pid, len(platforms[pid])))
	}
	b.WriteString("\n")

	b.WriteString("## 目录结构\n\n")
	b.WriteString("```\n")
	b.WriteString(fmt.Sprintf("%s/\n", ymd[0:4]))
	b.WriteString(fmt.Sprintf("└── %s/\n", ymd[4:6]))
	b.WriteString(fmt.Sprintf("    └── %s/\n", ymd))
	b.WriteString("        ├── README.md          # 本文件\n")
	for _, pid := range platformNames {
		b.WriteString(fmt.Sprintf("        ├── %s/           # %d 条\n", pid, len(platforms[pid])))
	}
	b.WriteString("```\n\n")

	b.WriteString("## 数据格式\n\n")
	b.WriteString("每个 `.md` 文件包含新闻元数据（来源、排名、链接）和原文正文内容。\n\n")

	if strings.TrimSpace(reportContent) != "" {
		b.WriteString("---\n\n")
		b.WriteString("## 当日行业 AI 研报\n\n")
		b.WriteString(reportContent)
		b.WriteString("\n")
	}

	return b.String()
}

func buildNewsMarkdown(item enrichedNews) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# %s\n\n", item.Title))
	sourceLabel := item.SourceName
	if sourceLabel == "" {
		sourceLabel = item.SourceID
	}
	b.WriteString(fmt.Sprintf("- **来源平台**: %s\n", sourceLabel))
	if item.Rank > 0 {
		b.WriteString(fmt.Sprintf("- **排名**: #%d\n", item.Rank))
	}
	if item.URL != "" {
		b.WriteString(fmt.Sprintf("- **原文链接**: %s\n", item.URL))
	}
	if item.MobileURL != "" && item.MobileURL != item.URL {
		b.WriteString(fmt.Sprintf("- **移动端链接**: %s\n", item.MobileURL))
	}
	b.WriteString(fmt.Sprintf("- **抓取时间**: %s\n", item.CapturedAt.Format("2006-01-02 15:04:05")))
	b.WriteString("\n---\n\n")

	// 正文内容
	if item.Fetched && item.Content != "" {
		b.WriteString("## 正文内容\n\n")
		b.WriteString(item.Content)
		b.WriteString("\n")
	} else if item.FetchErr != "" {
		b.WriteString("## 正文内容\n\n")
		b.WriteString(fmt.Sprintf("> 正文抓取失败: %s\n", item.FetchErr))
		b.WriteString(fmt.Sprintf("> 原文链接: %s\n", item.URL))
		b.WriteString("\n")
	} else {
		b.WriteString("## 正文内容\n\n")
		b.WriteString("> 未启用正文抓取或链接为空\n")
		b.WriteString(fmt.Sprintf("> 原文链接: %s\n", item.URL))
		b.WriteString("\n")
	}

	return b.String()
}

func sanitizeFilename(s string) string {
	s = strings.TrimSpace(s)
	replacer := strings.NewReplacer(
		"/", "_", "\\", "_", ":", "_", "*", "_", "?", "_",
		"\"", "_", "<", "_", ">", "_", "|", "_", "\n", "_",
		"\r", "_", "\t", "_",
	)
	s = replacer.Replace(s)
	for strings.Contains(s, "__") {
		s = strings.ReplaceAll(s, "__", "_")
	}
	s = strings.Trim(s, "_ .")
	if s == "" {
		s = "untitled"
	}
	runes := []rune(s)
	if len(runes) > 120 {
		s = string(runes[:120])
	}
	return s
}

func uniqueFilePath(path string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s_%d%s", base, i, ext)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

// pushToModelScope 将导出目录推送到 ModelScope 数据集仓库。year/month/ymd 构成仓库内的层级路径。
func pushToModelScope(exportDir, year, month, ymd, repo, token, gitUser, gitEmail string) error {
	tmpDir, err := os.MkdirTemp("", "modelscope_push_*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	cloneURL := fmt.Sprintf("https://oauth2:%s@www.modelscope.cn/datasets/%s.git", token, repo)
	repoDir := filepath.Join(tmpDir, "repo")

	l := applog.WithComponent("daily_export")
	l.Info(fmt.Sprintf("  [Git 1/4] Cloning repo: %s (depth=1)...", repo))

	cmd := exec.Command("git", "clone", "--depth", "1", cloneURL, repoDir)
	if err := runGitCmd(cmd, 120*time.Second); err != nil {
		l.Error("  [Git 1/4] Clone failed", zap.Error(err))
		return fmt.Errorf("git clone: %w", err)
	}
	l.Info("  [Git 1/4] Clone completed")

	dstDir := filepath.Join(repoDir, year, month, ymd)
	_ = os.RemoveAll(dstDir)
	if err := copyDir(exportDir, dstDir); err != nil {
		l.Error("  [Git 2/4] Copy export files failed", zap.Error(err))
		return fmt.Errorf("copy export dir: %w", err)
	}
	l.Info(fmt.Sprintf("  [Git 2/4] Copy completed: %s -> %s", exportDir, dstDir))

	if err := runGitCmd(exec.Command("git", "-C", repoDir, "config", "user.name", gitUser), 30*time.Second); err != nil {
		return fmt.Errorf("git config user.name: %w", err)
	}
	if err := runGitCmd(exec.Command("git", "-C", repoDir, "config", "user.email", gitEmail), 30*time.Second); err != nil {
		return fmt.Errorf("git config user.email: %w", err)
	}
	if err := runGitCmd(exec.Command("git", "-C", repoDir, "add", "-A"), 60*time.Second); err != nil {
		l.Error("  [Git 3/4] git add failed", zap.Error(err))
		return fmt.Errorf("git add: %w", err)
	}

	statusCmd := exec.Command("git", "-C", repoDir, "status", "--porcelain")
	statusOut, err := statusCmd.Output()
	if err != nil {
		return fmt.Errorf("git status: %w", err)
	}
	if strings.TrimSpace(string(statusOut)) == "" {
		l.Info("  [Git 3/4] No changes, already up to date")
		return nil
	}

	commitMsg := fmt.Sprintf("daily: %s", ymd)
	if err := runGitCmd(exec.Command("git", "-C", repoDir, "commit", "-m", commitMsg), 60*time.Second); err != nil {
		l.Error("  [Git 3/4] git commit failed", zap.Error(err))
		return fmt.Errorf("git commit: %w", err)
	}
	l.Info(fmt.Sprintf("  [Git 3/4] Commit completed: %s", commitMsg))

	for attempt := 1; attempt <= 3; attempt++ {
		l.Info(fmt.Sprintf("  [Git 4/4] Push attempt %d/3...", attempt))
		err := runGitCmd(exec.Command("git", "-C", repoDir, "push", "origin", "HEAD"), 120*time.Second)
		if err == nil {
			l.Info(fmt.Sprintf("  [Git 4/4] Push succeeded! repo=%s, ymd=%s", repo, ymd))
			return nil
		}
		if attempt < 3 {
			time.Sleep(5 * time.Second)
			_ = runGitCmd(exec.Command("git", "-C", repoDir, "pull", "--rebase", "origin", "HEAD"), 60*time.Second)
		}
	}
	return fmt.Errorf("git push failed after 3 attempts")
}

func runGitCmd(cmd *exec.Cmd, timeout time.Duration) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		return fmt.Errorf("git command timeout after %v: %s", timeout, strings.Join(cmd.Args, " "))
	}
}

func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0750); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		srcPath := filepath.Join(src, e.Name())
		dstPath := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			data, err := os.ReadFile(srcPath)
			if err != nil {
				return err
			}
			if err := os.WriteFile(dstPath, data, 0640); err != nil {
				return err
			}
		}
	}
	return nil
}
