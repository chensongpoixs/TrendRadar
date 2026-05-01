package ai

import (
	"context"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/trendradar/backend-go/pkg/config"
)

const (
	articleFetchTimeout   = 18 * time.Second
	articleMaxBodyBytes   = 2 * 1024 * 1024
	articleMaxTextRunes   = 12000
	articleSummaryMaxToks = 2048
)

var reHTMLStrip = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>|<style[^>]*>.*?</style>|<[^>]+>|&nbsp;`)
var reWS = regexp.MustCompile(`\s+`)

// SummarizeNewsArticle 拉取 url 正文（若可），结合标题与业界常见「读报」风格输出中文汇报式摘要；仅在用户主动调用分析接口时使用。
func SummarizeNewsArticle(ctx context.Context, title, rawURL, sourceName string) (summary string, fetched bool, textLen int, err error) {
	title = strings.TrimSpace(title)
	rawURL = strings.TrimSpace(rawURL)
	if title == "" || rawURL == "" {
		return "", false, 0, fmt.Errorf("title and url are required")
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", false, 0, fmt.Errorf("invalid url")
	}
	if host := u.Hostname(); host != "" {
		if ip := net.ParseIP(host); ip != nil {
			if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() {
				return "", false, 0, fmt.Errorf("url host not allowed")
			}
		} else if strings.EqualFold(host, "localhost") {
			return "", false, 0, fmt.Errorf("url host not allowed")
		}
	}

	bodyText, fetchErr := FetchArticlePlainText(ctx, rawURL)
	fetched = fetchErr == nil && strings.TrimSpace(bodyText) != ""
	if !fetched && fetchErr != nil {
		// 仍继续：仅基于标题做弱解读，并在 system 中说明
		bodyText = ""
	}
	textLen = utf8.RuneCountInString(bodyText)
	if textLen > articleMaxTextRunes {
		bodyText = string([]rune(bodyText)[:articleMaxTextRunes])
		textLen = articleMaxTextRunes
	}

	var user strings.Builder
	user.WriteString("请根据以下信息，用【中文】写一份面向行业从业者的「读报式」分析（目标读者：投资人/产品经理/技术决策者），结构建议包含：\n")
	user.WriteString("1）核心事实与产业背景 2）竞争格局与影响分析 3）可关注的后续动向。总篇幅控制在 400 字以内，不要编造事实。\n\n")
	user.WriteString("标题：")
	user.WriteString(title)
	user.WriteString("\n")
	if sourceName != "" {
		user.WriteString("来源：")
		user.WriteString(sourceName)
		user.WriteString("\n")
	}
	user.WriteString("链接：")
	user.WriteString(rawURL)
	user.WriteString("\n")
	if fetched {
		user.WriteString("\n以下为从页面抽取的正文片段（可能不完整）：\n")
		user.WriteString(bodyText)
	} else {
		user.WriteString("\n（未能可靠拉取正文，请仅依据标题与常识做保守、短小的提示，并说明信息有限。）\n")
	}

	client := NewAIClient()
	if client != nil {
		applyArticleAnalyzeHTTPDefaults(client)
	}
	msgs := []ChatMessage{
		{
			Role: "system",
			Content: "你是资深科技产业分析师，兼具媒体编辑的简洁表达与投研的严谨逻辑。只输出汇报正文，不要使用 markdown 代码围栏。若无法确定事实，须写「待核实」而非臆测。优先关注产业竞争格局、技术路线与商业模式维度的信息增量和潜在影响。",
		},
		{Role: "user", Content: user.String()},
	}
	out, err := client.ChatWithMaxOutput(msgs, articleSummaryMaxToks)
	if err != nil {
		return "", fetched, textLen, err
	}
	return strings.TrimSpace(out), fetched, textLen, nil
}

func applyArticleAnalyzeHTTPDefaults(c *AIClient) {
	if c == nil {
		return
	}
	cfg := config.Get()
	if cfg == nil {
		return
	}
	if c.timeout == 0 {
		tsec := cfg.AI.Timeout
		if tsec <= 0 {
			tsec = 120
		}
		c.timeout = time.Duration(tsec) * time.Second
		c.client.Timeout = c.timeout
	}
	if c.numRetries > 2 {
		c.numRetries = 1
	}
}

// FetchArticlePlainText 抓取 URL 对应的 HTML 页面，去标签后返回纯文本正文片段，限制在约 2MB 以内。
// 对于反爬严格的站点（知乎、百度等）会自动切换 UA 重试一次。
// 直抓失败时，通过 Jina AI Reader（服务端渲染 JS 页面）降级抓取。
func FetchArticlePlainText(ctx context.Context, rawURL string) (string, error) {
	// 模拟真实 Chrome 浏览器的完整请求头
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8,en-US;q=0.7")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Sec-Ch-Ua", `"Google Chrome";v="131", "Chromium";v="131", "Not_A Brand";v="24"`)
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Platform", `"Windows"`)
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	req.Header.Set("Dnt", "1")

	// 自动跟随重定向且携带 cookie
	cli := &http.Client{
		Timeout: articleFetchTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

	text, err := doFetchPlainText(cli, req)
	if err != nil {
		// 403/429/503 等反爬状态码：换一套 UA 重试一次
		if strings.Contains(err.Error(), "http status 403") ||
			strings.Contains(err.Error(), "http status 429") ||
			strings.Contains(err.Error(), "http status 503") {
			req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
			req.Header.Set("Sec-Ch-Ua-Platform", `"macOS"`)
			text, err = doFetchPlainText(cli, req)
		}
	}

	// 直抓成功且内容不像"需开启 JS"的空壳页面，直接返回
	if err == nil && text != "" && !isJSRequiredPage(text) {
		return text, nil
	}

	// 降级：通过 Jina AI Reader 抓取（服务端渲染 JS 页面）
	if jinaText, jinaErr := fetchViaJinaReader(ctx, rawURL); jinaErr == nil && jinaText != "" {
		return jinaText, nil
	}

	if err != nil {
		return "", err
	}
	return text, nil
}

// isJSRequiredPage 检测响应内容是否为"请启用 JavaScript"类空壳页面或无实质内容的 JSON 载荷
func isJSRequiredPage(text string) bool {
	lower := strings.ToLower(text)
	indicators := []string{
		"请启用javascript", "需要允许该网站执行javascript",
		"please enable javascript", "you need to enable javascript",
		"请开启javascript", "enable javascript in your browser",
		"请打开javascript", "您需要允许该网站执行",
	}
	for _, kw := range indicators {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	// 极短内容（<200 字符）且包含常见空壳标记
	if len([]rune(text)) < 200 {
		shellMarkers := []string{"noscript", "browser", "app__view", "js-support"}
		for _, m := range shellMarkers {
			if strings.Contains(lower, m) {
				return true
			}
		}
		// 纯 JSON 且无实质正文（如 {"ok":false} / {"status":"fail"}）
		if strings.HasPrefix(text, "{") && strings.HasSuffix(text, "}") {
			return true
		}
	}
	return false
}

// fetchViaJinaReader 通过 Jina AI Reader API 抓取页面正文（自动渲染 JS 页面，返回 Markdown）
// 对抖音、微博等重度 JS 页面尤其有效，因为 Jina 在服务端执行完整浏览器渲染。
func fetchViaJinaReader(ctx context.Context, rawURL string) (string, error) {
	jinaURL := "https://r.jina.ai/" + rawURL
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jinaURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/markdown,text/plain")
	req.Header.Set("X-Return-Format", "text")
	req.Header.Set("X-Timeout", "20")
	// 可选：若配置了 Jina API key 则带上（免费版可不用）
	if cfg := config.Get(); cfg != nil {
		if key := strings.TrimSpace(os.Getenv("JINA_API_KEY")); key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}

	cli := &http.Client{Timeout: 28 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		return "", fmt.Errorf("jina fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 403 || resp.StatusCode == 429 {
		return "", fmt.Errorf("jina rate limited (status %d)", resp.StatusCode)
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("jina http status %d", resp.StatusCode)
	}
	lim := io.LimitReader(resp.Body, articleMaxBodyBytes)
	b, err := io.ReadAll(lim)
	if err != nil {
		return "", fmt.Errorf("jina read: %w", err)
	}
	// Jina 返回 Markdown，去掉格式得到纯文本
	text := string(b)
	// 去掉 Jina 可能附加的页脚信息（Links/Further reading 等）
	if idx := strings.Index(text, "\n\n---\n"); idx > 0 {
		text = text[:idx]
	}
	// 去掉 Markdown 链接语法 [text](url) 保留 text
	reMDLink := regexp.MustCompile(`\[([^\]]*)\]\([^)]+\)`)
	text = reMDLink.ReplaceAllString(text, "$1")
	// 去掉 Markdown 标题标记、加粗等
	text = regexp.MustCompile(`(?m)^#{1,6}\s+`).ReplaceAllString(text, "")
	text = regexp.MustCompile(`\*{1,3}([^*]+)\*{1,3}`).ReplaceAllString(text, "$1")
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("jina returned empty body")
	}
	return text, nil
}

// FetchArticleWithFallback 先尝试主 URL，失败则用 mobileURL 降级再试，适用于微博/抖音等反爬严格的平台。
func FetchArticleWithFallback(ctx context.Context, url, mobileURL string) (string, error) {
	url = strings.TrimSpace(url)
	mobileURL = strings.TrimSpace(mobileURL)
	if url == "" && mobileURL == "" {
		return "", fmt.Errorf("both urls empty")
	}
	if url == "" {
		url = mobileURL
		mobileURL = ""
	}

	text, err := FetchArticlePlainText(ctx, url)
	if err == nil && text != "" {
		return text, nil
	}

	// 若主 URL 失败且有 mobileURL（且不同），用 mobileURL 再试
	if mobileURL != "" && mobileURL != url {
		text2, err2 := FetchArticlePlainText(ctx, mobileURL)
		if err2 == nil && text2 != "" {
			return text2, nil
		}
	}

	if err != nil {
		return "", err
	}
	return text, nil
}

func doFetchPlainText(cli *http.Client, req *http.Request) (string, error) {
	resp, err := cli.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return "", fmt.Errorf("http status %d", resp.StatusCode)
	}
	lim := io.LimitReader(resp.Body, articleMaxBodyBytes)
	b, err := io.ReadAll(lim)
	if err != nil {
		return "", err
	}
	s := string(b)
	s = reHTMLStrip.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = reWS.ReplaceAllString(s, " ")
	return strings.TrimSpace(s), nil
}
