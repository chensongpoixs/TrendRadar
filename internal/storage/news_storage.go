package storage

import (
	"strconv"
	"strings"
	"time"

	"github.com/trendradar/backend-go/internal/core"
	"github.com/trendradar/backend-go/pkg/model"
)

// NewsStorage 新闻存储层
type NewsStorage struct{}

// NewNewsStorage 创建新闻存储实例
func NewNewsStorage() *NewsStorage {
	return &NewsStorage{}
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

// GetLatestNews 获取最新一批新闻
func (s *NewsStorage) GetLatestNews(platformIDs []string) (map[string][]model.NewsItem, error) {
	db := core.GetDB()
	results := make(map[string][]model.NewsItem)

	for _, platformID := range platformIDs {
		var items []model.NewsItem
		if err := db.Where("source_id = ?", platformID).Order("crawl_time DESC").Limit(100).Find(&items).Error; err != nil {
			return nil, err
		}
		results[platformID] = items
	}

	return results, nil
}

// GetTrendingTopics 获取热门话题
func (s *NewsStorage) GetTrendingTopics(topN int, mode string) ([]model.Topic, error) {
	// TODO: 实现话题提取逻辑
	return nil, nil
}

// NormalizeURL 标准化 URL
func NormalizeURL(url string) string {
	// TODO: 实现 URL 标准化（移除动态参数等）
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
