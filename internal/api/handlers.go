package api

import (
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/trendradar/backend-go/internal/ai"
	"github.com/trendradar/backend-go/internal/crawler"
	"github.com/trendradar/backend-go/internal/storage"
	"github.com/trendradar/backend-go/pkg/config"
	"github.com/trendradar/backend-go/pkg/model"
)

// GetLatestNews 获取最新新闻
func GetLatestNews(c *gin.Context) {
	platformsParam := c.QueryArray("platforms")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	includeURL, _ := strconv.ParseBool(c.DefaultQuery("include_url", "false"))
	useAIFilter, _ := strconv.ParseBool(c.DefaultQuery("use_ai_filter", "false"))
	_ = platformsParam
	_ = limit
	_ = includeURL

	// 创建爬虫实例
	platformCrawler := crawler.NewPlatformCrawler()

	// 抓取数据
	results, idToName, failedIDs, err := platformCrawler.CrawlAll()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	// 可选 AI 兴趣筛选：默认关闭，避免接口耗时导致前端超时
	if useAIFilter {
		results = ai.ApplyFocusFilter(results)
	}

	// 保存到数据库
	newsStorage := storage.NewNewsStorage()
	crawlTime := time.Now()

	for platformID, items := range results {
		if err := newsStorage.SaveNewsData(platformID, items, crawlTime); err != nil {
			log.Printf("Failed to save data for %s: %v", platformID, err)
		}
	}

	// 按需求关闭内容 AI 分析：仅保留标题 AI 过滤
	aiSummary := gin.H{
		"enabled": false,
		"reason":  "content_ai_analysis_disabled",
	}

	// 返回结果
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"news":       results,
			"id_to_name": idToName,
			"failed_ids": failedIDs,
			"crawl_time": crawlTime.Format(time.RFC3339),
			"ai_analysis": aiSummary,
		},
	})
}

func buildAINewsSummary(results map[string][]model.NewsItem, idToName map[string]string) (gin.H, error) {
	cfg := config.Get()
	maxNews := cfg.AIAnalysis.MaxNewsForAnalysis
	if maxNews <= 0 {
		maxNews = 80
	}

	newsByPlatform := make(map[string][]ai.NewsItem)
	for platformID, items := range results {
		sourceName := idToName[platformID]
		for _, item := range items {
			newsByPlatform[platformID] = append(newsByPlatform[platformID], ai.NewsItem{
				Title:  item.Title,
				Rank:   item.Rank,
				Source: sourceName,
			})
		}
	}

	analyzer := ai.NewAnalyzer()
	analysis, err := analyzer.Analyze(&ai.AnalysisConfig{
		Mode:       cfg.AIAnalysis.Mode,
		IncludeRss: false,
		MaxNews:    maxNews,
	}, newsByPlatform, map[string][]ai.RSSItem{})
	if err != nil {
		return nil, err
	}

	return gin.H{
		"enabled":              true,
		"mode":                 cfg.AIAnalysis.Mode,
		"core_trends":          analysis.CoreTrends,
		"sentiment_controversy": analysis.SentimentControversy,
		"signals":              analysis.Signals,
		"outlook_strategy":     analysis.OutlookStrategy,
		"raw_response":         analysis.RawResponse,
	}, nil
}

// GetNewsByDate 按日期获取新闻
func GetNewsByDate(c *gin.Context) {
	date := c.Param("date")
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}

	platformsParam := c.QueryArray("platforms")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	_ = limit

	newsStorage := storage.NewNewsStorage()

	// 获取平台列表
	cfg := config.Get().Platforms
	var platformIDs []string
	if len(platformsParam) > 0 {
		platformIDs = platformsParam
	} else {
		for _, source := range cfg.Sources {
			if source.Enabled {
				platformIDs = append(platformIDs, source.ID)
			}
		}
	}

	results, err := newsStorage.GetTodayNews(platformIDs, date)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"news": results,
			"date": date,
		},
	})
}

// SearchNews 搜索新闻
func SearchNews(c *gin.Context) {
	query := c.Query("query")
	if query == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "query parameter is required",
		})
		return
	}

	searchMode := c.DefaultQuery("search_mode", "keyword")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	sortBy := c.DefaultQuery("sort_by", "relevance")

	// 解析日期范围
	dateRange := c.Query("date_range")
	var dateStart, dateEnd string
	if dateRange != "" {
		// 简单处理：如果是"今天"、"昨天"等自然语言，转换为日期
		switch dateRange {
		case "today":
			today := time.Now().Format("2006-01-02")
			dateStart, dateEnd = today, today
		case "yesterday":
			yesterday := time.Now().AddDate(0, 0, -1)
			yesterdayStr := yesterday.Format("2006-01-02")
			dateStart, dateEnd = yesterdayStr, yesterdayStr
		case "last_7_days":
			dateStart = time.Now().AddDate(0, 0, -7).Format("2006-01-02")
			dateEnd = time.Now().Format("2006-01-02")
		}
	}

	// 平台过滤
	platforms := c.QueryArray("platforms")

	// 执行搜索
	newsStorage := storage.NewNewsStorage()
	results, err := newsStorage.SearchNews(&model.SearchOptions{
		Query:     query,
		SearchMode: searchMode,
		DateStart:  dateStart,
		DateEnd:    dateEnd,
		Platforms:  platforms,
		Limit:      limit,
		SortBy:     sortBy,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"query":       query,
			"search_mode": searchMode,
			"results":     results,
			"total":       len(results),
		},
	})
}

// GetTrendingTopics 获取热门话题
func GetTrendingTopics(c *gin.Context) {
	topN, _ := strconv.Atoi(c.DefaultQuery("top_n", "10"))
	mode := c.DefaultQuery("mode", "current")

	newsStorage := storage.NewNewsStorage()
	topics, err := newsStorage.GetTopicStats(topN, mode)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"topics": topics,
			"mode":   mode,
		},
	})
}

// GetLatestRSS 获取最新 RSS
func GetLatestRSS(c *gin.Context) {
	feedsParam := c.QueryArray("feeds")
	days, _ := strconv.Atoi(c.DefaultQuery("days", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	_ = feedsParam
	_ = days
	_ = limit

	// 创建 RSS 爬虫
	rssCrawler := crawler.NewRSSCrawler()

	// 抓取数据
	results, idToName, failedIDs, err := rssCrawler.FetchAll()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	// 保存到数据库
	newsStorage := storage.NewNewsStorage()
	crawlTime := time.Now()

	for feedID, items := range results {
		if err := newsStorage.SaveRSSData(feedID, items, crawlTime); err != nil {
			log.Printf("Failed to save RSS data for %s: %v", feedID, err)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"rss":      results,
			"id_to_name": idToName,
			"failed_ids": failedIDs,
			"crawl_time": crawlTime.Format(time.RFC3339),
		},
	})
}

// SearchRSS 搜索 RSS
func SearchRSS(c *gin.Context) {
	keyword := c.Query("keyword")
	if keyword == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "keyword parameter is required",
		})
		return
	}

	feeds := c.QueryArray("feeds")
	days, _ := strconv.Atoi(c.DefaultQuery("days", "7"))

	newsStorage := storage.NewNewsStorage()
	results, err := newsStorage.SearchRSS(keyword, feeds, days)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"keyword": keyword,
			"results": results,
			"total":   len(results),
		},
	})
}

// GetRSSFeedsStatus 获取 RSS 源状态
func GetRSSFeedsStatus(c *gin.Context) {
	cfg := config.Get().RSS

	feeds := make(map[string]gin.H)
	for _, feed := range cfg.Feeds {
		feeds[feed.ID] = gin.H{
			"name":       feed.Name,
			"url":        feed.URL,
			"enabled":    feed.Enabled,
			"max_items":  feed.MaxItems,
			"max_age_days": feed.MaxAgeDays,
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"feeds": feeds,
			"total": len(cfg.Feeds),
		},
	})
}

// AnalyzeTopicTrend 分析话题趋势
func AnalyzeTopicTrend(c *gin.Context) {
	var input struct {
		Topic        string  `json:"topic"`
		AnalysisType string  `json:"analysis_type"`
		DateRange    string  `json:"date_range"`
		Granularity  string  `json:"granularity"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	if input.Topic == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "topic is required",
		})
		return
	}

	// 设置默认值
	if input.AnalysisType == "" {
		input.AnalysisType = "trend"
	}
	if input.Granularity == "" {
		input.Granularity = "day"
	}

	// 查询话题相关新闻
	newsStorage := storage.NewNewsStorage()
	opts := &model.SearchOptions{
		Query:    input.Topic,
		SearchMode: "keyword",
		Limit:    200,
	}

	// 解析日期范围
	if input.DateRange != "" {
		switch input.DateRange {
		case "last_7_days":
			opts.DateStart = time.Now().AddDate(0, 0, -7).Format("2006-01-02")
		case "last_30_days":
			opts.DateStart = time.Now().AddDate(0, 0, -30).Format("2006-01-02")
		}
	}

	newsItems, err := newsStorage.SearchNews(opts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	// 按日期分组统计
	trendData := make(map[string]int)
	for _, item := range newsItems {
		date := item.CrawlTime.Format("2006-01-02")
		trendData[date]++
	}

	// 转换为数组格式
	type TrendPoint struct {
		Date  string `json:"date"`
		Value int    `json:"value"`
	}
	var trendPoints []TrendPoint
	for date, count := range trendData {
		trendPoints = append(trendPoints, TrendPoint{Date: date, Value: count})
	}

	// 排序
	for i := 0; i < len(trendPoints)-1; i++ {
		for j := i + 1; j < len(trendPoints); j++ {
			if trendPoints[j].Date < trendPoints[i].Date {
				trendPoints[i], trendPoints[j] = trendPoints[j], trendPoints[i]
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"topic":        input.Topic,
			"analysis_type": input.AnalysisType,
			"trend":        trendPoints,
			"total_news":   len(newsItems),
		},
	})
}

// AnalyzeSentiment 分析情感倾向
func AnalyzeSentiment(c *gin.Context) {
	var input struct {
		Topic     string   `json:"topic"`
		Platforms []string `json:"platforms"`
		DateRange string   `json:"date_range"`
		Limit     int      `json:"limit"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	if input.Limit <= 0 {
		input.Limit = 50
	}

	// 查询相关新闻
	newsStorage := storage.NewNewsStorage()
	opts := &model.SearchOptions{
		Query:    input.Topic,
		SearchMode: "keyword",
		Platforms: input.Platforms,
		Limit:    input.Limit,
	}

	if input.DateRange == "last_7_days" {
		opts.DateStart = time.Now().AddDate(0, 0, -7).Format("2006-01-02")
	}

	newsItems, err := newsStorage.SearchNews(opts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	// 简单的情感分析（基于关键词）
	positiveKeywords := []string{"好", "棒", "赞", "优秀", "成功", "突破", "创新", "领先"}
	negativeKeywords := []string{"差", "问题", "争议", "风险", "危机", "失败", "下跌"}

	positiveCount := 0
	negativeCount := 0
	neutralCount := 0

	for _, item := range newsItems {
		titleLower := strings.ToLower(item.Title)
		pos := 0
		neg := 0
		for _, kw := range positiveKeywords {
			if strings.Contains(titleLower, kw) {
				pos++
			}
		}
		for _, kw := range negativeKeywords {
			if strings.Contains(titleLower, kw) {
				neg++
			}
		}
		if pos > neg {
			positiveCount++
		} else if neg > pos {
			negativeCount++
		} else {
			neutralCount++
		}
	}

	total := len(newsItems)
	var sentiment string
	if total > 0 {
		if float64(positiveCount)/float64(total) > 0.6 {
			sentiment = "positive"
		} else if float64(negativeCount)/float64(total) > 0.6 {
			sentiment = "negative"
		} else {
			sentiment = "neutral"
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"topic":     input.Topic,
			"sentiment": sentiment,
			"distribution": gin.H{
				"positive": positiveCount,
				"negative": negativeCount,
				"neutral":  neutralCount,
			},
			"percentages": gin.H{
				"positive":  float64(positiveCount) / float64(max(total, 1)) * 100,
				"negative":  float64(negativeCount) / float64(max(total, 1)) * 100,
				"neutral":   float64(neutralCount) / float64(max(total, 1)) * 100,
			},
			"total_news": total,
		},
	})
}

// AggregateNews 聚合新闻
func AggregateNews(c *gin.Context) {
	var input struct {
		DateRange          *string  `json:"date_range"`
		Platforms          []string `json:"platforms"`
		SimilarityThreshold float64 `json:"similarity_threshold"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	// 设置默认相似度阈值
	similarityThreshold := input.SimilarityThreshold
	if similarityThreshold <= 0 {
		similarityThreshold = 0.7
	}

	// 查询新闻（最近 7 天）
	newsStorage := storage.NewNewsStorage()
	opts := &model.SearchOptions{
		Query:     "",
		SearchMode: "keyword",
		Platforms: input.Platforms,
		Limit:     200,
	}

	if input.DateRange != nil {
		switch *input.DateRange {
		case "last_7_days":
			opts.DateStart = time.Now().AddDate(0, 0, -7).Format("2006-01-02")
		case "last_30_days":
			opts.DateStart = time.Now().AddDate(0, 0, -30).Format("2006-01-02")
		}
	}

	newsItems, err := newsStorage.SearchNews(opts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	// 简单的新闻聚类（基于关键词相似度）
	type Cluster struct {
		Title    string
		Items    []model.NewsItem
		Keywords []string
	}

	clusters := make([]Cluster, 0)
	used := make(map[int]bool)

	for i := 0; i < len(newsItems); i++ {
		if used[i] {
			continue
		}

		item := newsItems[i]
		keywords := extractKeywords(item.Title)

		cluster := Cluster{
			Title:    item.Title,
			Items:    []model.NewsItem{item},
			Keywords: keywords,
		}
		used[i] = true

		// 查找相似新闻
		for j := i + 1; j < len(newsItems); j++ {
			if used[j] {
				continue
			}

			otherItem := newsItems[j]
			otherKeywords := extractKeywords(otherItem.Title)

			// 计算关键词交集比例
			similarity := calculateKeywordSimilarity(keywords, otherKeywords)
			if similarity >= similarityThreshold {
				cluster.Items = append(cluster.Items, otherItem)
				used[j] = true
			}
		}

		clusters = append(clusters, cluster)
	}

	// 转换为响应格式
	type ClusterResponse struct {
		Title       string         `json:"title"`
		Keywords    []string       `json:"keywords"`
		ItemCount   int            `json:"item_count"`
		Items       []model.NewsItem `json:"items"`
		PlatformIDs []string       `json:"platform_ids"`
	}

	var clusterResponses []ClusterResponse
	for _, cluster := range clusters {
		platformIDs := make(map[string]bool)
		for _, item := range cluster.Items {
			platformIDs[item.SourceID] = true
		}
		ids := make([]string, 0, len(platformIDs))
		for id := range platformIDs {
			ids = append(ids, id)
		}

		clusterResponses = append(clusterResponses, ClusterResponse{
			Title:       cluster.Title,
			Keywords:    cluster.Keywords,
			ItemCount:   len(cluster.Items),
			Items:       cluster.Items,
			PlatformIDs: ids,
		})
	}

	// 按新闻数量排序
	for i := 0; i < len(clusterResponses)-1; i++ {
		for j := i + 1; j < len(clusterResponses); j++ {
			if clusterResponses[j].ItemCount > clusterResponses[i].ItemCount {
				clusterResponses[i], clusterResponses[j] = clusterResponses[j], clusterResponses[i]
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"clusters":         clusterResponses,
			"cluster_count":    len(clusterResponses),
			"total_news":       len(newsItems),
			"similarity_threshold": similarityThreshold,
		},
	})
}

// extractKeywords 从标题提取关键词
func extractKeywords(title string) []string {
	// 简单实现：按常见分隔符分割
	separators := []string{" ", "-", "—", "|", "·", ",", ","}
	result := title
	for _, sep := range separators {
		result = strings.ReplaceAll(result, sep, " ")
	}

	words := strings.Fields(result)
	keywords := make([]string, 0)
	for _, word := range words {
		if len(word) > 1 {
			keywords = append(keywords, strings.ToLower(word))
		}
	}
	return keywords
}

// calculateKeywordSimilarity 计算关键词相似度（Jaccard 相似度）
func calculateKeywordSimilarity(keywords1, keywords2 []string) float64 {
	if len(keywords1) == 0 || len(keywords2) == 0 {
		return 0
	}

	// 转换为 map
	set1 := make(map[string]bool)
	set2 := make(map[string]bool)
	for _, kw := range keywords1 {
		set1[kw] = true
	}
	for _, kw := range keywords2 {
		set2[kw] = true
	}

	// 计算交集
	intersection := 0
	for kw := range set1 {
		if set2[kw] {
			intersection++
		}
	}

	// 计算并集
	union := len(set1) + len(set2) - intersection

	if union == 0 {
		return 0
	}

	return float64(intersection) / float64(union)
}

// ComparePeriods 对比分析不同时期
func ComparePeriods(c *gin.Context) {
	var input struct {
		Period1Start string `json:"period1_start"`
		Period1End   string `json:"period1_end"`
		Period2Start string `json:"period2_start"`
		Period2End   string `json:"period2_end"`
		Platforms    []string `json:"platforms"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	// 默认对比：最近 7 天 vs 前 7 天
	now := time.Now()
	period1End := now
	period1Start := now.AddDate(0, 0, -7)
	period2End := now.AddDate(0, 0, -7)
	period2Start := now.AddDate(0, 0, -14)

	if input.Period1Start != "" && input.Period1End != "" {
		if start, err := time.Parse("2006-01-02", input.Period1Start); err == nil {
			period1Start = start
		}
		if end, err := time.Parse("2006-01-02", input.Period1End); err == nil {
			period1End = end
		}
	}

	if input.Period2Start != "" && input.Period2End != "" {
		if start, err := time.Parse("2006-01-02", input.Period2Start); err == nil {
			period2Start = start
		}
		if end, err := time.Parse("2006-01-02", input.Period2End); err == nil {
			period2End = end
		}
	}

	// 查询两个时期的新闻
	newsStorage := storage.NewNewsStorage()

	period1Opts := &model.SearchOptions{
		Query:     "",
		SearchMode: "keyword",
		DateStart:  period1Start.Format("2006-01-02"),
		DateEnd:    period1End.Format("2006-01-02"),
		Platforms: input.Platforms,
		Limit:     500,
	}

	period2Opts := &model.SearchOptions{
		Query:     "",
		SearchMode: "keyword",
		DateStart:  period2Start.Format("2006-01-02"),
		DateEnd:    period2End.Format("2006-01-02"),
		Platforms: input.Platforms,
		Limit:     500,
	}

	period1News, _ := newsStorage.SearchNews(period1Opts)
	period2News, _ := newsStorage.SearchNews(period2Opts)

	// 统计关键词频率
	period1Keywords := countKeywords(period1News)
	period2Keywords := countKeywords(period2News)

	// 计算变化
	type KeywordChange struct {
		Keyword   string  `json:"keyword"`
		Count1    int     `json:"count1"`
		Count2    int     `json:"count2"`
		Change    int     `json:"change"`
		ChangePct float64 `json:"change_pct"`
	}

	var changes []KeywordChange
	allKeywords := make(map[string]bool)
	for kw := range period1Keywords {
		allKeywords[kw] = true
	}
	for kw := range period2Keywords {
		allKeywords[kw] = true
	}

	for kw := range allKeywords {
		count1 := period1Keywords[kw]
		count2 := period2Keywords[kw]
		change := count2 - count1
		changePct := 0.0
		if count1 > 0 {
			changePct = float64(change) / float64(count1) * 100
		}

		changes = append(changes, KeywordChange{
			Keyword:   kw,
			Count1:    count1,
			Count2:    count2,
			Change:    change,
			ChangePct: changePct,
		})
	}

	// 按变化幅度排序
	for i := 0; i < len(changes)-1; i++ {
		for j := i + 1; j < len(changes); j++ {
			if abs(changes[j].ChangePct) > abs(changes[i].ChangePct) {
				changes[i], changes[j] = changes[j], changes[i]
			}
		}
	}

	// 取前 20 个变化最大的关键词
	limit := 20
	if len(changes) < limit {
		limit = len(changes)
	}
	topChanges := changes[:limit]

	// 平台分布对比
	platform1Dist := countPlatformDistribution(period1News)
	platform2Dist := countPlatformDistribution(period2News)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"period1": gin.H{
				"start": period1Start.Format("2006-01-02"),
				"end":   period1End.Format("2006-01-02"),
				"total": len(period1News),
			},
			"period2": gin.H{
				"start": period2Start.Format("2006-01-02"),
				"end":   period2End.Format("2006-01-02"),
				"total": len(period2News),
			},
			"summary": gin.H{
				"news_change":    len(period2News) - len(period1News),
				"news_change_pct": float64(len(period2News)-len(period1News)) / float64(max(len(period1News), 1)) * 100,
			},
			"keyword_changes": topChanges,
			"platform_distribution": gin.H{
				"period1": platform1Dist,
				"period2": platform2Dist,
			},
		},
	})
}

// countKeywords 统计关键词频率
func countKeywords(newsItems []model.NewsItem) map[string]int {
	keywordCount := make(map[string]int)
	for _, item := range newsItems {
		keywords := extractKeywords(item.Title)
		for _, kw := range keywords {
			keywordCount[kw]++
		}
	}
	return keywordCount
}

// countPlatformDistribution 统计平台分布
func countPlatformDistribution(newsItems []model.NewsItem) map[string]int {
	dist := make(map[string]int)
	for _, item := range newsItems {
		dist[item.SourceID]++
	}
	return dist
}

// abs 返回绝对值
func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// GetSystemStatus 获取系统状态
func GetSystemStatus(c *gin.Context) {
	cfg := config.Get()

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"version":    cfg.App.Version,
			"environment": cfg.App.Environment,
			"timezone":   cfg.App.Timezone,
			"uptime":     time.Since(time.Now()).String(),
			"database":   "connected",
		},
	})
}

// GetCurrentConfig 获取当前配置
func GetCurrentConfig(c *gin.Context) {
	section := c.DefaultQuery("section", "all")
	cfg := config.Get()

	var result gin.H
	switch section {
	case "crawler":
		result = gin.H{
			"platforms": cfg.Platforms,
			"rss":       cfg.RSS,
		}
	case "push":
		result = gin.H{
			"notification": cfg.Notification,
		}
	case "keywords":
		// TODO: 加载关键词配置
		result = gin.H{}
	case "weights":
		result = gin.H{
			"weight": cfg.Advanced.Weight,
		}
	default:
		result = gin.H{
			"app":        cfg.App,
			"server":     cfg.Server,
			"scheduler":  cfg.Scheduler,
			"platforms":  cfg.Platforms,
			"rss":        cfg.RSS,
			"report":     cfg.Report,
			"filter":     cfg.Filter,
			"ai":         cfg.AI,
			"notification": cfg.Notification,
			"storage":    cfg.Storage,
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    result,
	})
}

// TriggerCrawl 触发抓取任务
func TriggerCrawl(c *gin.Context) {
	var input struct {
		Platforms   []string `json:"platforms"`
		SaveToLocal bool     `json:"save_to_local"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	// 触发抓取
	platformCrawler := crawler.NewPlatformCrawler()
	results, idToName, failedIDs, err := platformCrawler.CrawlAll()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"news":      results,
			"id_to_name": idToName,
			"failed_ids": failedIDs,
		},
	})
}

// SyncFromRemote 从远程同步数据
func SyncFromRemote(c *gin.Context) {
	var input struct {
		Days int `json:"days"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		input.Days = 7
	}

	// TODO: 实现远程同步
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"days": input.Days,
		},
	})
}

// GetStorageStatus 获取存储状态
func GetStorageStatus(c *gin.Context) {
	cfg := config.Get().Storage

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"backend": cfg.Backend,
			"formats": cfg.Formats,
			"local":   cfg.Local,
			"remote":  cfg.Remote,
		},
	})
}

// ListAvailableDates 列出可用日期
func ListAvailableDates(c *gin.Context) {
	source := c.DefaultQuery("source", "both")

	// TODO: 实现日期列表查询
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"source": source,
			"dates":  []string{},
		},
	})
}

// MCPHandle MCP 协议处理
func MCPHandle(c *gin.Context) {
	// TODO: 实现 MCP 协议处理
	c.JSON(http.StatusOK, gin.H{
		"jsonrpc": "2.0",
		"result":  gin.H{},
		"id":      1,
	})
}
