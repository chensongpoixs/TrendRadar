package crawler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/trendradar/backend-go/pkg/config"
	"github.com/trendradar/backend-go/pkg/logger"
	"github.com/trendradar/backend-go/pkg/model"
	"go.uber.org/zap"
)

// PlatformCrawler 平台热榜爬虫
type PlatformCrawler struct {
	client        *http.Client
	requestInterval time.Duration
	useProxy      bool
	proxyURL      string
}

// NewsNowAPIResponse NewsNow API 响应结构
type NewsNowAPIResponse struct {
	Status  string `json:"status"`
	Items   []NewsNowItem `json:"items"`
}

type NewsNowItem struct {
	Title    string `json:"title"`
	URL      string `json:"url"`
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
		useProxy: cfg.Advanced.Crawler.UseProxy,
		proxyURL: cfg.Advanced.Crawler.DefaultProxy,
	}
}

// FetchData 获取单个平台数据
func (c *PlatformCrawler) FetchData(platformID string) ([]model.NewsItem, error) {
	url := fmt.Sprintf("https://newsnow.busiyi.world/api/s?id=%s&latest", platformID)
	lg := logger.WithComponent("crawler")
	lg.Debug("hotlist fetch start", zap.String("platform_id", platformID), zap.String("url", url))

	var items []model.NewsItem
	var lastError error

	// 重试逻辑
	for attempt := 1; attempt <= 3; attempt++ {
		items, lastError = c.fetchWithRetry(url, platformID)
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
func (c *PlatformCrawler) fetchWithRetry(url, platformID string) ([]model.NewsItem, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json,text/plain,*/*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

	// 设置代理
	if c.useProxy && c.proxyURL != "" {
		transport := &http.Transport{
			Proxy: http.ProxyURL(c.parseProxyURL()),
		}
		c.client = &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		}
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
			URL:        item.URL,
			MobileURL:  item.MobileURL,
			CrawlTime:  time.Now(),
		})
	}

	logger.WithComponent("crawler").Info("hotlist fetch done", zap.String("platform_id", platformID), zap.Int("items", len(items)))
	return items, nil
}

// CrawlAll 抓取所有平台数据
func (c *PlatformCrawler) CrawlAll() (map[string][]model.NewsItem, map[string]string, []string, error) {
	cfg := config.Get()

	results := make(map[string][]model.NewsItem)
	idToName := make(map[string]string)
	var failedIDs []string
	var wg sync.WaitGroup
	var mu sync.Mutex

	// 创建信号量控制并发
	sem := make(chan struct{}, 5) // 最多 5 个并发

	for _, source := range cfg.Platforms.Sources {

		wg.Add(1)
		idToName[source.ID] = source.Name

		go func(platformID, platformName string) {
			defer wg.Done()
			sem <- struct{}{} // 获取信号量
			defer func() { <-sem }() // 释放信号量

			items, err := c.FetchData(platformID)
			if err != nil {
				mu.Lock()
				failedIDs = append(failedIDs, platformID)
				mu.Unlock()
				logger.WithComponent("crawler").Error("crawl platform failed", zap.String("platform_id", platformID), zap.Error(err))
				return
			}

			mu.Lock()
			results[platformID] = items
			mu.Unlock()

			// 请求间隔
			if c.requestInterval > 0 {
				time.Sleep(c.requestInterval)
			}
		}(source.ID, source.Name)
	}

	wg.Wait()

	return results, idToName, failedIDs, nil
}

// parseProxyURL 解析代理 URL，支持 http://、https://、socks5:// 协议
func (c *PlatformCrawler) parseProxyURL() *url.URL {
	proxyURL := c.proxyURL
	// 如果没有协议前缀，添加 http://
	if proxyURL != "" && !strings.HasPrefix(proxyURL, "http://") && !strings.HasPrefix(proxyURL, "https://") && !strings.HasPrefix(proxyURL, "socks5://") {
		proxyURL = "http://" + proxyURL
	}
	if proxyURL == "" {
		return nil
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		logger.WithComponent("crawler").Warn("failed to parse proxy URL", zap.String("url", proxyURL), zap.Error(err))
		return nil
	}
	return parsed
}
