package crawler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/trendradar/backend-go/internal/storage"
	"github.com/trendradar/backend-go/pkg/config"
	"github.com/trendradar/backend-go/pkg/logger"
	"github.com/trendradar/backend-go/pkg/model"
	"go.uber.org/zap"
)

// PlatformCrawler 平台热榜爬虫
type PlatformCrawler struct {
	client          *http.Client
	requestInterval time.Duration
	useProxy        bool
	proxyURL        string
}

// NewsNowAPIResponse NewsNow API 响应结构
type NewsNowAPIResponse struct {
	Status string        `json:"status"`
	Items  []NewsNowItem `json:"items"`
}

type NewsNowItem struct {
	Title     string `json:"title"`
	URL       string `json:"url"`
	MobileURL string `json:"mobileUrl"`
}

// NewPlatformCrawler 创建平台爬虫实例
func NewPlatformCrawler() *PlatformCrawler {
	cfg := config.Get()

	return &PlatformCrawler{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		requestInterval: time.Duration(cfg.Advanced.Crawler.RequestInterval) * time.Millisecond,
		useProxy:        cfg.Advanced.Crawler.UseProxy,
		proxyURL:        cfg.Advanced.Crawler.DefaultProxy,
	}
}

// FetchData 获取单个平台数据
func (c *PlatformCrawler) FetchData(platformID string, platformName string) ([]model.NewsItem, error) {
	url := fmt.Sprintf("https://newsnow.busiyi.world/api/s?id=%s&latest", platformID)
	lg := logger.WithComponent("crawler")
	lg.Debug("hotlist fetch start", zap.String("platform_id", platformID), zap.String("url", url))

	var items []model.NewsItem
	var lastError error

	// 重试逻辑
	for attempt := 1; attempt <= 3; attempt++ {
		items, lastError = c.fetchWithRetry(url, platformID, platformName)
		if lastError == nil {
			break
		}

		lg.Warn("hotlist attempt failed",
			zap.String("platform_id", platformID), zap.Int("attempt", attempt), zap.Error(lastError))

		if attempt < 3 {
			// 指数退避
			waitTime := time.Duration(attempt*2) * time.Second
			time.Sleep(waitTime)
		}
	}

	return items, lastError
}

// fetchWithRetry 执行实际抓取
func (c *PlatformCrawler) fetchWithRetry(url, platformID, platformName string) ([]model.NewsItem, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json,text/plain,*/*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

	// 设置代理
	if c.useProxy && c.proxyURL != "" {
		// 代理配置
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var apiResp NewsNowAPIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, err
	}

	if apiResp.Status != "success" && apiResp.Status != "cache" {
		return nil, fmt.Errorf("api returned unexpected status: %s", apiResp.Status)
	}

	// 转换为 NewsItem
	var items []model.NewsItem
	for i, item := range apiResp.Items {
		items = append(items, model.NewsItem{
			Title:      item.Title,
			SourceID:   platformID,
			Rank:       i + 1,
			SourceName: platformName,
			URL:        item.URL,
			MobileURL:  item.MobileURL,
			CrawlTime:  time.Now(),
		})
	}

	logger.WithComponent("crawler").Info("hotlist fetch done", zap.String("platform_id", platformID), zap.Int("items", len(items)))
	return items, nil
}

// CrawlAll 抓取所有启用平台数据，并记录每个源的最近一次健康状态。
func (c *PlatformCrawler) CrawlAll() (map[string][]model.NewsItem, map[string]string, []string, error) {
	cfg := config.Get()

	results := make(map[string][]model.NewsItem)
	idToName := make(map[string]string)
	var failedIDs []string
	var healthRows []storage.SourceHealthInput
	var wg sync.WaitGroup
	var mu sync.Mutex

	sem := make(chan struct{}, 5) // 最多 5 个并发

	for _, source := range cfg.Platforms.Sources {
		if !source.Enabled {
			continue
		}

		wg.Add(1)
		idToName[source.ID] = source.Name

		go func(platformID, platformName string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			startedAt := time.Now()
			items, err := c.FetchData(platformID, platformName)
			finishedAt := time.Now()
			status := storage.SourceStatusSuccess
			if err != nil {
				mu.Lock()
				failedIDs = append(failedIDs, platformID)
				healthRows = append(healthRows, storage.SourceHealthInput{
					SourceType:   storage.SourceTypePlatform,
					SourceID:     platformID,
					SourceName:   platformName,
					Enabled:      true,
					Status:       storage.SourceStatusFailed,
					ItemCount:    0,
					LatencyMS:    finishedAt.Sub(startedAt).Milliseconds(),
					ErrorMessage: err.Error(),
					StartedAt:    startedAt,
					FinishedAt:   finishedAt,
				})
				mu.Unlock()
				logger.WithComponent("crawler").Error("crawl platform failed", zap.String("platform_id", platformID), zap.Error(err))
				return
			}
			if len(items) == 0 {
				status = storage.SourceStatusEmpty
			}

			mu.Lock()
			results[platformID] = items
			healthRows = append(healthRows, storage.SourceHealthInput{
				SourceType: storage.SourceTypePlatform,
				SourceID:   platformID,
				SourceName: platformName,
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
		}(source.ID, source.Name)
	}

	wg.Wait()
	if err := storage.NewDiagnosticsStorage().RecordSourceRuns(healthRows); err != nil {
		logger.WithComponent("crawler").Warn("record platform source health failed", zap.Error(err))
	}

	return results, idToName, failedIDs, nil
}
