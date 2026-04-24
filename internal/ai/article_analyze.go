package ai

import (
	"context"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
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

	bodyText, fetchErr := fetchArticlePlainText(ctx, rawURL)
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
	user.WriteString("请根据以下信息，用【中文】写一份简要的「读报式」汇报（面向行业读者），结构建议包含：\n")
	user.WriteString("1）核心事实与背景 2）要点与影响 3）若信息不足请明确说明。总篇幅控制在 400 字以内，不要编造事实。\n\n")
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
			Content: "你是资深行业媒体编辑。只输出汇报正文，不要使用 markdown 代码围栏。若无法确定事实，须写「待核实」而非臆测。",
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

func fetchArticlePlainText(ctx context.Context, rawURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; rv:109.0) Gecko/20100101 Firefox/115.0")
	req.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.5")

	cli := &http.Client{Timeout: articleFetchTimeout}
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
