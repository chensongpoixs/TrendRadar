package crawler

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/mmcdole/gofeed"
	"github.com/trendradar/backend-go/internal/storage"
	"github.com/trendradar/backend-go/pkg/config"
	"github.com/trendradar/backend-go/pkg/logger"
	"github.com/trendradar/backend-go/pkg/model"
	"go.uber.org/zap"
)

// RSSCrawler RSS 爬虫
type RSSCrawler struct {
	client          *http.Client
	requestInterval time.Duration
	timeout         time.Duration
}

// NewRSSCrawler 创建 RSS 爬虫实例
func NewRSSCrawler() *RSSCrawler {
	adv := config.Get().Advanced.RSS

	return &RSSCrawler{
		client: &http.Client{
			Timeout: time.Duration(adv.Timeout) * time.Second,
		},
		requestInterval: time.Duration(adv.RequestInterval) * time.Millisecond,
		timeout:         time.Duration(adv.Timeout) * time.Second,
	}
}

// FetchAll 抓取所有启用 RSS 源，并记录每个源的最近一次健康状态。
func (c *RSSCrawler) FetchAll() (map[string][]model.RSSItem, map[string]string, []string, error) {
	cfg := config.Get().RSS

	results := make(map[string][]model.RSSItem)
	idToName := make(map[string]string)
	var failedIDs []string
	var healthRows []storage.SourceHealthInput
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, feed := range cfg.Feeds {
		if !feed.Enabled {
			continue
		}

		wg.Add(1)
		idToName[feed.ID] = feed.Name

		go func(feedID, feedName, feedURL string, maxItems int) {
			defer wg.Done()
			startedAt := time.Now()
			items, err := c.fetchFeed(feedURL, feedID, feedName, maxItems)
			finishedAt := time.Now()
			status := storage.SourceStatusSuccess
			if err != nil {
				mu.Lock()
				failedIDs = append(failedIDs, feedID)
				healthRows = append(healthRows, storage.SourceHealthInput{
					SourceType:   storage.SourceTypeRSS,
					SourceID:     feedID,
					SourceName:   feedName,
					Enabled:      true,
					Status:       storage.SourceStatusFailed,
					ItemCount:    0,
					LatencyMS:    finishedAt.Sub(startedAt).Milliseconds(),
					ErrorMessage: err.Error(),
					StartedAt:    startedAt,
					FinishedAt:   finishedAt,
				})
				mu.Unlock()
				logger.WithComponent("crawler").Error("rss feed failed", zap.String("feed_id", feedID), zap.Error(err))
				return
			}
			if len(items) == 0 {
				status = storage.SourceStatusEmpty
			}

			mu.Lock()
			results[feedID] = items
			healthRows = append(healthRows, storage.SourceHealthInput{
				SourceType: storage.SourceTypeRSS,
				SourceID:   feedID,
				SourceName: feedName,
				Enabled:    true,
				Status:     status,
				ItemCount:  len(items),
				LatencyMS:  finishedAt.Sub(startedAt).Milliseconds(),
				StartedAt:  startedAt,
				FinishedAt: finishedAt,
			})
			mu.Unlock()

			if c.requestInterval > 0 {
				time.Sleep(c.requestInterval)
			}
		}(feed.ID, feed.Name, feed.URL, feed.MaxItems)
	}

	wg.Wait()
	if err := storage.NewDiagnosticsStorage().RecordSourceRuns(healthRows); err != nil {
		logger.WithComponent("crawler").Warn("record rss source health failed", zap.Error(err))
	}

	return results, idToName, failedIDs, nil
}

// fetchFeed 抓取单个 RSS 源
func (c *RSSCrawler) fetchFeed(url, feedID, feedName string, maxItems int) ([]model.RSSItem, error) {
	lg := logger.WithComponent("crawler")
	lg.Debug("rss fetch", zap.String("feed_id", feedID), zap.String("url", url))

	// 发起 HTTP 请求
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// 读取响应体
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// 解析 RSS
	feedParser := gofeed.NewParser()
	feed, err := feedParser.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	// 转换为 RSSItem
	var items []model.RSSItem
	count := 0
	for _, entry := range feed.Items {
		if maxItems > 0 && count >= maxItems {
			break
		}

		item := model.RSSItem{
			Title:     entry.Title,
			FeedID:    feedID,
			FeedName:  feedName,
			URL:       entry.Link,
			Summary:   entry.Description,
			Author:    "",
			CrawlTime: time.Now(),
		}
		if entry.Author != nil {
			item.Author = entry.Author.Name
		}

		// 解析发布时间
		if entry.PublishedParsed != nil {
			item.PublishedAt = entry.PublishedParsed.Format(time.RFC3339)
		}

		items = append(items, item)
		count++
	}

	lg.Info("rss fetch done", zap.String("feed_id", feedID), zap.Int("items", len(items)))
	return items, nil
}
