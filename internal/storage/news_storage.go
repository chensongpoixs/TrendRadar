package storage

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/trendradar/backend-go/internal/core"
	"github.com/trendradar/backend-go/pkg/config"
	"github.com/trendradar/backend-go/pkg/model"
	"gorm.io/gorm"
)

// NewsStorage 新闻存储层
type NewsStorage struct{}

// NewNewsStorage 创建新闻存储实例
func NewNewsStorage() *NewsStorage {
	return &NewsStorage{}
}

// PartitionCrawlByPersistedItems 将本批热榜按是否已在 news_items 中存在（同 source_id + NormalizeURL(url)）拆开：
//   - forAI：尚未落库，需送 LLM 做兴趣过滤；
//   - skipPersisted：本批中已在库中的链接，本小时不再送 AI，原样与过滤结果合并。
// 与 SaveNewsData 内使用的 NormalizeURL 一致。
func (s *NewsStorage) PartitionCrawlByPersistedItems(crawl map[string][]model.NewsItem) (forAI, skipPersisted map[string][]model.NewsItem, nForAI, nSkip int, err error) {
	forAI = make(map[string][]model.NewsItem)
	skipPersisted = make(map[string][]model.NewsItem)
	db := core.GetDB()
	for platformID, items := range crawl {
		if len(items) == 0 {
			continue
		}
		var urlRows []string
		if perr := db.Model(&model.NewsItem{}).Where("source_id = ?", platformID).Pluck("url", &urlRows).Error; perr != nil {
			return nil, nil, 0, 0, perr
		}
		exist := make(map[string]struct{}, len(urlRows))
		for _, u := range urlRows {
			exist[NormalizeURL(u)] = struct{}{}
		}
		for _, it := range items {
			u := strings.TrimSpace(it.URL)
			if u == "" {
				forAI[platformID] = append(forAI[platformID], it)
				nForAI++
				continue
			}
			if _, ok := exist[NormalizeURL(u)]; ok {
				skipPersisted[platformID] = append(skipPersisted[platformID], it)
				nSkip++
			} else {
				forAI[platformID] = append(forAI[platformID], it)
				nForAI++
			}
		}
	}
	return
}

// SaveNewsData 保存新闻数据
func (s *NewsStorage) SaveNewsData(platformID string, items []model.NewsItem, crawlTime time.Time) error {
	db := core.GetDB()

	// 获取当前时间的所有新闻
	var existingItems []model.NewsItem
	if err := db.Where("source_id = ? AND DATE(crawl_time) = DATE(?)", platformID, crawlTime).Find(&existingItems).Error; err != nil {
		return err
	}

	// 构建现有新闻的 URL 映射
	existingMap := make(map[string]model.NewsItem)
	for _, item := range existingItems {
		normalizedURL := NormalizeURL(item.URL)
		existingMap[normalizedURL] = item
	}

	// 处理新抓取的新闻
	for _, item := range items {
		normalizedURL := NormalizeURL(item.URL)

		if existingItem, exists := existingMap[normalizedURL]; exists {
			// 更新现有新闻
			existingItem.Rank = item.Rank
			existingItem.CrawlTime = crawlTime
			existingItem.LastTime = crawlTime
			existingItem.Count++

			// 更新排名历史
			err := db.Create(&model.RankHistory{
				NewsItemID: existingItem.ID,
				Rank:       item.Rank,
				CrawlTime:  crawlTime,
			}).Error
			if err != nil {
				return err
			}

			db.Save(&existingItem)
			delete(existingMap, normalizedURL)
		} else {
			// 插入新新闻
			item.FirstTime = crawlTime
			item.LastTime = crawlTime
			item.Ranks = "[" + strconv.Itoa(item.Rank) + "]"

			if err := db.Create(&item).Error; err != nil {
				return err
			}

			// 记录排名历史
			db.Create(&model.RankHistory{
				NewsItemID: item.ID,
				Rank:       item.Rank,
				CrawlTime:  crawlTime,
			})
		}
	}

	// 检测脱榜新闻（上次在榜但这次不在）
	for _, existingItem := range existingMap {
		// 插入脱榜记录（rank=0）
		db.Create(&model.RankHistory{
			NewsItemID: existingItem.ID,
			Rank:       0, // 0 表示脱榜
			CrawlTime:  crawlTime,
		})
	}

	if err := s.appendHotlistSnapshots(platformID, items, crawlTime); err != nil {
		return err
	}
	s.pruneOldHotlistSnapshots()
	return nil
}

// SaveRSSData 保存 RSS 数据
func (s *NewsStorage) SaveRSSData(feedID string, items []model.RSSItem, crawlTime time.Time) error {
	db := core.GetDB()

	for _, item := range items {
		// 检查是否已存在
		var existing model.RSSItem
		result := db.Where("url = ?", item.URL).First(&existing)

		if result.Error == nil {
			// 更新现有 RSS 项
			existing.LastTime = crawlTime
			existing.Count++
			db.Save(&existing)
		} else {
			// 插入新 RSS 项
			item.FirstTime = crawlTime
			item.LastTime = crawlTime
			db.Create(&item)
		}
	}

	return nil
}

// GetTodayNews 获取当天新闻
func (s *NewsStorage) GetTodayNews(platformIDs []string, date string) (map[string][]model.NewsItem, error) {
	db := core.GetDB()
	results := make(map[string][]model.NewsItem)

	for _, platformID := range platformIDs {
		var items []model.NewsItem
		if err := db.Where("source_id = ? AND DATE(crawl_time) = ?", platformID, date).Find(&items).Error; err != nil {
			return nil, err
		}
		results[platformID] = items
	}

	return results, nil
}

// GetLatestNews 从库中读取各平台「最近一次整批定时任务」写入的同 crawl_time 快照，按 rank 升序。不访问外网。
// limit 为每平台条数上限；返回各平台中较晚的 lastCrawl 作为整次展示的参考时间。
func (s *NewsStorage) GetLatestNews(platformIDs []string, limit int) (map[string][]model.NewsItem, time.Time, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	db := core.GetDB()
	results := make(map[string][]model.NewsItem)
	var lastCrawl time.Time
	for _, platformID := range platformIDs {
		var probe model.NewsItem
		err := db.Where("source_id = ?", platformID).Order("crawl_time DESC").First(&probe).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				results[platformID] = []model.NewsItem{}
				continue
			}
			return nil, time.Time{}, err
		}
		ct := probe.CrawlTime
		var items []model.NewsItem
		if err := db.Where("source_id = ? AND crawl_time = ?", platformID, ct).Order("rank ASC").Limit(limit).Find(&items).Error; err != nil {
			return nil, time.Time{}, err
		}
		results[platformID] = items
		if ct.After(lastCrawl) {
			lastCrawl = ct
		}
	}
	return results, lastCrawl, nil
}

// GetLatestRSSFromDB 从库中读取各源最近一次整批抓取的同 crawl_time 条目。不访问外网。
func (s *NewsStorage) GetLatestRSSFromDB(feedIDs []string, limit int) (map[string][]model.RSSItem, time.Time, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	db := core.GetDB()
	out := make(map[string][]model.RSSItem)
	var lastCrawl time.Time
	for _, feedID := range feedIDs {
		var probe model.RSSItem
		err := db.Where("feed_id = ?", feedID).Order("crawl_time DESC").First(&probe).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				out[feedID] = []model.RSSItem{}
				continue
			}
			return nil, time.Time{}, err
		}
		ct := probe.CrawlTime
		var items []model.RSSItem
		if err := db.Where("feed_id = ? AND crawl_time = ?", feedID, ct).Order("crawl_time DESC, id DESC").Limit(limit).Find(&items).Error; err != nil {
			return nil, time.Time{}, err
		}
		out[feedID] = items
		if ct.After(lastCrawl) {
			lastCrawl = ct
		}
	}
	return out, lastCrawl, nil
}

// GetTrendingTopics 获取热门话题
func (s *NewsStorage) GetTrendingTopics(topN int, mode string) ([]model.Topic, error) {
	// TODO: 实现话题提取逻辑
	return nil, nil
}

// NormalizeURL 标准化 URL，用于去重和比较
// 移除跟踪参数、片段标识符、尾部斜杠等
func NormalizeURL(url string) string {
	if url == "" {
		return ""
	}

	// 1. 去除前后空白
	url = strings.TrimSpace(url)

	// 2. 转换为小写（统一大小写）
	url = strings.ToLower(url)

	// 3. 移除 URL 片段（# 后面的部分）
	if idx := strings.Index(url, "#"); idx != -1 {
		url = url[:idx]
	}

	// 4. 移除常见的跟踪参数
	// 保留核心路径和必要参数
	trackParams := []string{
		"utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content",
		"utm_id", "utm_referrer", "utm_source_platform", "utm_creative_format",
		"share", "share_id", "share_token", "share_uid", "share_session",
		"from", "source", "src", "ref", "referrer", "sid", "session_id",
		"ts", "t", "d", "e", "i", "page", "position",
		"click", "click_id", "clickid", "_bs", "aid", "bid", "cid",
	}

	// 5. 解析并过滤参数
	if idx := strings.Index(url, "?"); idx != -1 {
		baseURL := url[:idx]
		queryParams := url[idx+1:]

		// 分离参数
		pairs := strings.Split(queryParams, "&")
		var keptParams []string
		for _, pair := range pairs {
			if pair == "" {
				continue
			}
			key := strings.SplitN(pair, "=", 2)[0]
			isTrackParam := false
			for _, trackParam := range trackParams {
				if strings.HasPrefix(key, trackParam) {
					isTrackParam = true
					break
				}
			}
			if !isTrackParam {
				keptParams = append(keptParams, pair)
			}
		}

		if len(keptParams) > 0 {
			url = baseURL + "?" + strings.Join(keptParams, "&")
		} else {
			url = baseURL
		}
	}

	// 6. 移除尾部斜杠（但保留根路径 /）
	if len(url) > 1 && strings.HasSuffix(url, "/") {
		url = strings.TrimSuffix(url, "/")
	}

	// 7. 规范化路径中的重复斜杠（保留 :// 协议前缀）
	// 分离协议、路径和查询参数
	protocol := ""
	query := ""
	if idx := strings.Index(url, "://"); idx != -1 {
		protocol = url[:idx+3] // 包括 ://
		url = url[idx+3:]
	}
	if idx := strings.Index(url, "?"); idx != -1 {
		query = url[idx:]
		url = url[:idx]
	}

	// 移除路径中的重复斜杠（/// → // → /）
	for strings.Contains(url, "///") {
		url = strings.ReplaceAll(url, "///", "//")
	}
	// 移除路径中多余的 //
	for strings.Contains(url, "//") {
		url = strings.ReplaceAll(url, "//", "/")
	}

	// 重新组合
	if protocol != "" {
		url = protocol + url
	}
	url += query

	return url
}

// IsURLTracked 检查 URL 是否包含跟踪参数
func IsURLTracked(url string) bool {
	trackParams := []string{
		"utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content",
		"share", "share_id", "share_token", "ref", "referrer", "sid",
	}

	if idx := strings.Index(url, "?"); idx != -1 {
		queryParams := url[idx+1:]
		pairs := strings.Split(queryParams, "&")
		for _, pair := range pairs {
			if pair == "" {
				continue
			}
			key := strings.SplitN(pair, "=", 2)[0]
			for _, trackParam := range trackParams {
				if strings.HasPrefix(key, trackParam) {
					return true
				}
			}
		}
	}
	return false
}

// ExtractBaseURL 提取 URL 的基础部分（不含参数）
func ExtractBaseURL(url string) string {
	if idx := strings.Index(url, "?"); idx != -1 {
		return url[:idx]
	}
	if idx := strings.Index(url, "#"); idx != -1 {
		return url[:idx]
	}
	return url
}

// SearchNews 搜索新闻
func (s *NewsStorage) SearchNews(opts *model.SearchOptions) ([]model.NewsItem, error) {
	db := core.GetDB()
	query := db.Model(&model.NewsItem{})

	// 关键词搜索
	if opts.Query != "" {
		switch opts.SearchMode {
		case "fuzzy":
			// 模糊搜索
			query = query.Where("title LIKE ?", "%"+opts.Query+"%")
		case "entity":
			// 实体搜索（简化的关键词搜索）
			query = query.Where("title LIKE ?", "%"+opts.Query+"%")
		default:
			// 精确关键词搜索
			keywords := strings.Split(opts.Query, " ")
			for _, keyword := range keywords {
				query = query.Where("title LIKE ?", "%"+keyword+"%")
			}
		}
	}

	// 日期范围过滤
	if opts.DateStart != "" {
		query = query.Where("DATE(crawl_time) >= ?", opts.DateStart)
	}
	if opts.DateEnd != "" {
		query = query.Where("DATE(crawl_time) <= ?", opts.DateEnd)
	}

	// 平台过滤
	if len(opts.Platforms) > 0 {
		query = query.Where("source_id IN ?", opts.Platforms)
	}

	// 排序
	switch opts.SortBy {
	case "weight":
		query = query.Order("count DESC, rank ASC")
	case "date":
		query = query.Order("crawl_time DESC")
	default:
		query = query.Order("crawl_time DESC")
	}

	// 限制返回数量
	limit := opts.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	query = query.Limit(limit)

	var items []model.NewsItem
	if err := query.Find(&items).Error; err != nil {
		return nil, err
	}

	return items, nil
}

// SearchRSS 搜索 RSS
func (s *NewsStorage) SearchRSS(keyword string, feeds []string, days int) ([]model.RSSItem, error) {
	db := core.GetDB()
	query := db.Model(&model.RSSItem{})

	// 关键词搜索
	if keyword != "" {
		query = query.Where("title LIKE ? OR summary LIKE ?", "%"+keyword+"%", "%"+keyword+"%")
	}

	// RSS 源过滤
	if len(feeds) > 0 {
		query = query.Where("feed_id IN ?", feeds)
	}

	// 天数过滤
	if days > 0 {
		startDate := time.Now().AddDate(0, 0, -days).Format("2006-01-02")
		query = query.Where("DATE(crawl_time) >= ?", startDate)
	}

	query = query.Order("crawl_time DESC").Limit(50)

	var items []model.RSSItem
	if err := query.Find(&items).Error; err != nil {
		return nil, err
	}

	return items, nil
}

// GetTopicStats 获取话题统计
func (s *NewsStorage) GetTopicStats(topN int, mode string) ([]model.Topic, error) {
	// 简化实现：按出现次数统计关键词
	db := core.GetDB()

	var startDate string
	if mode == "daily" {
		startDate = time.Now().Format("2006-01-02")
	}

	// 获取新闻数据
	var items []model.NewsItem
	query := db.Model(&model.NewsItem{})
	if mode == "daily" {
		query = query.Where("DATE(crawl_time) >= ?", startDate)
	}
	query = query.Order("crawl_time DESC")

	if err := query.Find(&items).Error; err != nil {
		return nil, err
	}

	// 统计关键词频率
	wordFreq := make(map[string]int)
	for _, item := range items {
		// 简单分词：按空格和标点分割
		words := strings.Fields(item.Title)
		for _, word := range words {
			if len(word) > 1 {
				wordFreq[word]++
			}
		}
	}

	// 转换为 Topic 列表并排序
	type freqItem struct {
		word  string
		count int
	}
	var freqItems []freqItem
	for word, count := range wordFreq {
		if count >= 2 {
			freqItems = append(freqItems, freqItem{word, count})
		}
	}

	// 按频率排序
	for i := 0; i < len(freqItems)-1; i++ {
		for j := i + 1; j < len(freqItems); j++ {
			if freqItems[j].count > freqItems[i].count {
				freqItems[i], freqItems[j] = freqItems[j], freqItems[i]
			}
		}
	}

	// 取 Top N
	limit := topN
	if limit > len(freqItems) {
		limit = len(freqItems)
	}

	topics := make([]model.Topic, limit)
	for i := 0; i < limit; i++ {
		topics[i] = model.Topic{
			Word:  freqItems[i].word,
			Count: freqItems[i].count,
		}
	}

	return topics, nil
}

func appTimeLocation() *time.Location {
	tz := config.Get().App.Timezone
	if tz == "" {
		return time.Local
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.Local
	}
	return loc
}

// appendHotlistSnapshots 将本批热榜按应用时区写入日/小时桶（追加型快照）
func (s *NewsStorage) appendHotlistSnapshots(platformID string, items []model.NewsItem, crawlTime time.Time) error {
	if len(items) == 0 {
		return nil
	}
	loc := appTimeLocation()
	t := crawlTime.In(loc)
	dateStr := t.Format("2006-01-02")
	hour := t.Hour()

	db := core.GetDB()
	rows := make([]model.HotlistSnapshot, 0, len(items))
	for _, it := range items {
		rows = append(rows, model.HotlistSnapshot{
			DateLocal:  dateStr,
			HourLocal:  hour,
			SourceID:   platformID,
			SourceName: it.SourceName,
			Title:      it.Title,
			URL:        it.URL,
			MobileURL:  it.MobileURL,
			Rank:       it.Rank,
			CapturedAt: crawlTime,
		})
	}
	return db.CreateInBatches(rows, 200).Error
}

func (s *NewsStorage) pruneOldHotlistSnapshots() {
	retention := config.Get().Storage.Local.RetentionDays
	if retention <= 0 {
		retention = 30
	}
	loc := appTimeLocation()
	cut := time.Now().In(loc).AddDate(0, 0, -retention)
	cutoff := cut.Format("2006-01-02")
	_ = core.GetDB().Where("date_local < ?", cutoff).Delete(&model.HotlistSnapshot{}).Error
}

// SnapshotDateInfo 有快照数据的自然日
type SnapshotDateInfo struct {
	Date     string `json:"date"`
	RowCount int64  `json:"row_count"`
}

// GetSnapshotAvailableDates 返回有热榜快照的日期（新→旧），按应用时区下的 date_local
func (s *NewsStorage) GetSnapshotAvailableDates(days int) ([]SnapshotDateInfo, error) {
	if days <= 0 || days > 365 {
		days = 60
	}
	db := core.GetDB()
	var rows []struct {
		DateLocal string
		RowCount  int64
	}
	err := db.Model(&model.HotlistSnapshot{}).
		Select("date_local AS date_local, COUNT(*) AS row_count").
		Group("date_local").
		Order("date_local DESC").
		Limit(days).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]SnapshotDateInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, SnapshotDateInfo{Date: r.DateLocal, RowCount: r.RowCount})
	}
	return out, nil
}

// SnapshotHourStat 某天内某小时统计
type SnapshotHourStat struct {
	Hour          int    `json:"hour"`
	RowCount      int64  `json:"row_count"`
	FirstCaptured string `json:"first_captured"`
	LastCaptured  string `json:"last_captured"`
}

// parseAggregatedCapturedAt 将 SQLite 对 MIN/MAX(captured_at) 返回的字符串（或已是 RFC3339）规范为 UTC RFC3339。
func parseAggregatedCapturedAt(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05.999",
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.Local); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return s
}

// GetSnapshotDaySummary 按日汇总：各小时行数与首尾抓取时间
func (s *NewsStorage) GetSnapshotDaySummary(dateLocal string) ([]SnapshotHourStat, int64, error) {
	db := core.GetDB()
	// SQLite 对 MIN/MAX(时间列) 常以 string 返回，不能 Scan 到 time.Time（会报 unsupported Scan）
	type agg struct {
		Hour          int    `gorm:"column:hour"`
		RowCount      int64  `gorm:"column:row_count"`
		FirstCaptured string `gorm:"column:first_captured"`
		LastCaptured  string `gorm:"column:last_captured"`
	}
	var aggs []agg
	err := db.Model(&model.HotlistSnapshot{}).
		Select("hour_local AS hour, COUNT(*) AS row_count, MIN(captured_at) AS first_captured, MAX(captured_at) AS last_captured").
		Where("date_local = ?", dateLocal).
		Group("hour_local").
		Order("hour_local ASC").
		Scan(&aggs).Error
	if err != nil {
		return nil, 0, err
	}
	var total int64
	for _, a := range aggs {
		total += a.RowCount
	}
	out := make([]SnapshotHourStat, 0, len(aggs))
	for _, a := range aggs {
		out = append(out, SnapshotHourStat{
			Hour:          a.Hour,
			RowCount:      a.RowCount,
			FirstCaptured: parseAggregatedCapturedAt(a.FirstCaptured),
			LastCaptured:  parseAggregatedCapturedAt(a.LastCaptured),
		})
	}
	return out, total, nil
}

// GetSnapshotForHour 返回某小时全部快照，按同一 source+url 仅保留「最晚一次抓取」
func (s *NewsStorage) GetSnapshotForHour(dateLocal string, hour int, platformIDs []string) ([]model.HotlistSnapshot, error) {
	if hour < 0 || hour > 23 {
		return nil, errors.New("invalid hour")
	}
	db := core.GetDB()
	q := db.Where("date_local = ? AND hour_local = ?", dateLocal, hour)
	if len(platformIDs) > 0 {
		q = q.Where("source_id IN ?", platformIDs)
	}
	var raw []model.HotlistSnapshot
	if err := q.Order("captured_at DESC, rank ASC, title ASC").Find(&raw).Error; err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	deduped := make([]model.HotlistSnapshot, 0, len(raw))
	for _, r := range raw {
		key := r.SourceID + "\x00" + NormalizeURL(r.URL)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, r)
	}
	sort.Slice(deduped, func(i, j int) bool {
		ri, rj := deduped[i].Rank, deduped[j].Rank
		if ri != rj {
			if ri == 0 {
				return false
			}
			if rj == 0 {
				return true
			}
			return ri < rj
		}
		return deduped[i].Title < deduped[j].Title
	})
	return deduped, nil
}

// defaultDayDigestMaxRunes 整日标题流默认最大字符数（按 rune 计），以适配大上下文模型输入预算
const defaultDayDigestMaxRunes = 120_000

// SnapshotDayDigestResult 某日多时刻快照按 URL 去重、排序后拼成 AI 可读的「标题流」
type SnapshotDayDigestResult struct {
	Digest        string
	UniqueTitles  int
	RawRowCount   int
	Truncated     bool
	MaxRunesLimit int
}

// BuildSnapshotDayDigest 读取该日所有 hotlist_snapshots，按 source+url 仅保留最晚一次抓取，再按小时与 rank 排序，拼为文本行并截断至 maxRunes（≤0 时用 defaultDayDigestMaxRunes）。
func (s *NewsStorage) BuildSnapshotDayDigest(dateLocal string, platformIDs []string, maxRunes int) (*SnapshotDayDigestResult, error) {
	if maxRunes <= 0 {
		maxRunes = defaultDayDigestMaxRunes
	}
	db := core.GetDB()
	q := db.Where("date_local = ?", dateLocal)
	if len(platformIDs) > 0 {
		q = q.Where("source_id IN ?", platformIDs)
	}
	var raw []model.HotlistSnapshot
	if err := q.Order("captured_at DESC, hour_local ASC, rank ASC, title ASC").Find(&raw).Error; err != nil {
		return nil, err
	}
	nRaw := len(raw)
	seen := make(map[string]struct{})
	deduped := make([]model.HotlistSnapshot, 0, nRaw/2)
	for _, r := range raw {
		key := r.SourceID + "\x00" + NormalizeURL(r.URL)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, r)
	}
	sort.Slice(deduped, func(i, j int) bool {
		hi, hj := deduped[i].HourLocal, deduped[j].HourLocal
		if hi != hj {
			return hi < hj
		}
		ri, rj := deduped[i].Rank, deduped[j].Rank
		if ri != rj {
			if ri == 0 {
				return false
			}
			if rj == 0 {
				return true
			}
			return ri < rj
		}
		return strings.Compare(deduped[i].Title, deduped[j].Title) < 0
	})
	var b strings.Builder
	sep := " | "
	for _, r := range deduped {
		name := strings.TrimSpace(r.SourceName)
		if name == "" {
			name = r.SourceID
		}
		line := fmt.Sprintf("%02d:00 %s %s", r.HourLocal, name+sep, r.Title)
		if strings.TrimSpace(r.URL) != "" {
			line += " " + strings.TrimSpace(r.URL)
		}
		line += "\n"
		if _, err := b.WriteString(line); err != nil {
			return nil, err
		}
	}
	out := b.String()
	truncated := false
	if utf8.RuneCountInString(out) > maxRunes {
		truncated = true
		rs := []rune(out)
		if len(rs) > maxRunes {
			out = string(rs[:maxRunes])
		}
		footer := fmt.Sprintf("\n[输入已按 %d 字截断；本日去重后共 %d 条标题。]\n", maxRunes, len(deduped))
		out = strings.TrimSpace(out) + "\n" + footer
	}
	return &SnapshotDayDigestResult{
		Digest:        strings.TrimSpace(out),
		UniqueTitles:  len(deduped),
		RawRowCount:   nRaw,
		Truncated:     truncated,
		MaxRunesLimit: maxRunes,
	}, nil
}

// GetDayIndustryReport 读取已缓存的整日行业 AI 研报；无记录时返回 (nil, nil)
func (s *NewsStorage) GetDayIndustryReport(dateLocal string) (*model.DayIndustryReport, error) {
	var row model.DayIndustryReport
	err := core.GetDB().Where("date_local = ?", dateLocal).First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

// SaveDayIndustryReport 写入或更新某日研报缓存
func (s *NewsStorage) SaveDayIndustryReport(dateLocal, content, modelName string) error {
	db := core.GetDB()
	var row model.DayIndustryReport
	err := db.Where("date_local = ?", dateLocal).First(&row).Error
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return db.Create(&model.DayIndustryReport{
			DateLocal: dateLocal,
			Content:   content,
			Model:     modelName,
		}).Error
	}
	row.Content = content
	row.Model = modelName
	return db.Save(&row).Error
}
