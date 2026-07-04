package webfetch

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/PuerkitoBio/goquery"
	"github.com/trendradar/backend-go/pkg/config"
)

const (
	defaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	defaultReaderURL = "https://r.jina.ai"
)

var (
	reHTMLNoise = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>|<style[^>]*>.*?</style>|<noscript[^>]*>.*?</noscript>|<svg[^>]*>.*?</svg>`)
	reHTMLTag   = regexp.MustCompile(`(?s)<[^>]+>`)
	reWS        = regexp.MustCompile(`\s+`)
	reMDLink    = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]+\)`)
)

// Options controls one webpage fetch. The default path does not bypass login
// walls, paywalls, CAPTCHA, robots denial, or explicit access blocks.
type Options struct {
	Timeout           time.Duration
	Retries           int
	Backoff           time.Duration
	MinTextChars      int
	MaxTextRunes      int
	MaxBodyBytes      int64
	UserAgent         string
	JinaEnabled       bool
	JinaTimeout       time.Duration
	JinaBaseURL       string
	RespectRobots     bool
	AllowPrivateHosts bool
}

type Attempt struct {
	Method        string
	StatusCode    int
	FinalURL      string
	TextLen       int
	Err           string
	BlockedReason string
}

type Result struct {
	URL           string
	Success       bool
	Method        string
	FinalURL      string
	Title         string
	Text          string
	StatusCode    int
	BlockedReason string
	Attempts      []Attempt
}

func (r Result) Error() error {
	if r.Success {
		return nil
	}
	if r.BlockedReason != "" {
		return fmt.Errorf("web fetch blocked: %s", r.BlockedReason)
	}
	for i := len(r.Attempts) - 1; i >= 0; i-- {
		if r.Attempts[i].Err != "" {
			return fmt.Errorf("web fetch failed via %s: %s", r.Attempts[i].Method, r.Attempts[i].Err)
		}
		if r.Attempts[i].StatusCode > 0 {
			return fmt.Errorf("web fetch failed via %s: http status %d", r.Attempts[i].Method, r.Attempts[i].StatusCode)
		}
	}
	return fmt.Errorf("web fetch failed")
}

func DefaultOptions() Options {
	opt := Options{
		Timeout:       18 * time.Second,
		Retries:       2,
		Backoff:       1200 * time.Millisecond,
		MinTextChars:  180,
		MaxTextRunes:  12000,
		MaxBodyBytes:  2 * 1024 * 1024,
		UserAgent:     defaultUserAgent,
		JinaEnabled:   true,
		JinaTimeout:   28 * time.Second,
		JinaBaseURL:   defaultReaderURL,
		RespectRobots: false,
	}
	cfg := config.Get()
	if cfg == nil {
		return opt
	}
	wf := cfg.Advanced.WebFetch
	if wf.Timeout > 0 {
		opt.Timeout = time.Duration(wf.Timeout) * time.Second
	}
	if wf.Retries >= 0 {
		opt.Retries = wf.Retries
	}
	if wf.BackoffMS > 0 {
		opt.Backoff = time.Duration(wf.BackoffMS) * time.Millisecond
	}
	if wf.MinTextChars > 0 {
		opt.MinTextChars = wf.MinTextChars
	}
	if wf.MaxTextRunes > 0 {
		opt.MaxTextRunes = wf.MaxTextRunes
	}
	if wf.MaxBodyMB > 0 {
		opt.MaxBodyBytes = int64(wf.MaxBodyMB) * 1024 * 1024
	}
	if strings.TrimSpace(wf.UserAgent) != "" {
		opt.UserAgent = strings.TrimSpace(wf.UserAgent)
	}
	opt.JinaEnabled = wf.JinaEnabled
	if wf.JinaTimeout > 0 {
		opt.JinaTimeout = time.Duration(wf.JinaTimeout) * time.Second
	}
	if strings.TrimSpace(wf.JinaBaseURL) != "" {
		opt.JinaBaseURL = strings.TrimRight(strings.TrimSpace(wf.JinaBaseURL), "/")
	}
	opt.RespectRobots = wf.RespectRobots
	return opt
}

func FetchPlainText(ctx context.Context, rawURL string) (string, error) {
	result := Fetch(ctx, rawURL)
	if !result.Success {
		return strings.TrimSpace(result.Text), result.Error()
	}
	return strings.TrimSpace(result.Text), nil
}

func FetchWithFallback(ctx context.Context, rawURL, mobileURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	mobileURL = strings.TrimSpace(mobileURL)
	if rawURL == "" && mobileURL == "" {
		return "", fmt.Errorf("both urls empty")
	}
	if rawURL == "" {
		return FetchPlainText(ctx, mobileURL)
	}
	text, err := FetchPlainText(ctx, rawURL)
	if err == nil && strings.TrimSpace(text) != "" {
		return text, nil
	}
	if mobileURL != "" && mobileURL != rawURL {
		text2, err2 := FetchPlainText(ctx, mobileURL)
		if err2 == nil && strings.TrimSpace(text2) != "" {
			return text2, nil
		}
	}
	return text, err
}

func Fetch(ctx context.Context, rawURL string) Result {
	return FetchWithOptions(ctx, rawURL, DefaultOptions())
}

func FetchWithOptions(ctx context.Context, rawURL string, opt Options) Result {
	opt = normalizeOptions(opt)
	result := Result{URL: rawURL}
	if err := validateURL(rawURL, opt.AllowPrivateHosts); err != nil {
		result.Attempts = append(result.Attempts, Attempt{Method: "validate", Err: err.Error()})
		result.BlockedReason = "invalid_url"
		return result
	}
	if opt.RespectRobots && !robotsAllowed(ctx, rawURL, opt) {
		result.Attempts = append(result.Attempts, Attempt{Method: "robots", BlockedReason: "robots_denied"})
		result.BlockedReason = "robots_denied"
		return result
	}

	direct := fetchDirect(ctx, rawURL, opt)
	result.Attempts = append(result.Attempts, direct.Attempts...)
	if direct.Success {
		direct.Attempts = result.Attempts
		return direct
	}
	if opt.JinaEnabled && shouldTryJina(direct) {
		reader := fetchJina(ctx, rawURL, opt)
		reader.Attempts = append(result.Attempts, reader.Attempts...)
		if reader.Success {
			return reader
		}
		result.Attempts = reader.Attempts
		best := bestResult(direct, reader)
		best.Attempts = result.Attempts
		return best
	}
	direct.Attempts = result.Attempts
	return direct
}

func normalizeOptions(opt Options) Options {
	def := Options{
		Timeout:      18 * time.Second,
		Retries:      2,
		Backoff:      1200 * time.Millisecond,
		MinTextChars: 180,
		MaxTextRunes: 12000,
		MaxBodyBytes: 2 * 1024 * 1024,
		UserAgent:    defaultUserAgent,
		JinaEnabled:  true,
		JinaTimeout:  28 * time.Second,
		JinaBaseURL:  defaultReaderURL,
	}
	if opt.Timeout <= 0 {
		opt.Timeout = def.Timeout
	}
	if opt.Backoff <= 0 {
		opt.Backoff = def.Backoff
	}
	if opt.MinTextChars <= 0 {
		opt.MinTextChars = def.MinTextChars
	}
	if opt.MaxTextRunes <= 0 {
		opt.MaxTextRunes = def.MaxTextRunes
	}
	if opt.MaxBodyBytes <= 0 {
		opt.MaxBodyBytes = def.MaxBodyBytes
	}
	if strings.TrimSpace(opt.UserAgent) == "" {
		opt.UserAgent = def.UserAgent
	}
	if opt.JinaTimeout <= 0 {
		opt.JinaTimeout = def.JinaTimeout
	}
	if strings.TrimSpace(opt.JinaBaseURL) == "" {
		opt.JinaBaseURL = def.JinaBaseURL
	}
	if opt.Retries < 0 {
		opt.Retries = 0
	}
	return opt
}

func fetchDirect(ctx context.Context, rawURL string, opt Options) Result {
	jar, _ := cookiejar.New(nil)
	cli := &http.Client{
		Timeout: opt.Timeout,
		Jar:     jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 8 {
				return fmt.Errorf("too many redirects")
			}
			setBrowserHeaders(req, opt.UserAgent)
			return nil
		},
	}
	result := Result{URL: rawURL, Method: "direct"}
	for attempt := 0; attempt <= opt.Retries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			result.Attempts = append(result.Attempts, Attempt{Method: "direct", Err: err.Error()})
			return result
		}
		setBrowserHeaders(req, opt.UserAgent)
		resp, err := cli.Do(req)
		if err != nil {
			result.Attempts = append(result.Attempts, Attempt{Method: "direct", Err: err.Error()})
			if attempt < opt.Retries {
				sleepBackoff(ctx, opt.Backoff, attempt, "")
				continue
			}
			return result
		}
		body, readErr := readLimited(resp.Body, opt.MaxBodyBytes)
		_ = resp.Body.Close()
		if readErr != nil {
			result.Attempts = append(result.Attempts, Attempt{Method: "direct", StatusCode: resp.StatusCode, FinalURL: resp.Request.URL.String(), Err: readErr.Error()})
			return result
		}
		title, text := extractReadableText(body)
		text = trimRunes(text, opt.MaxTextRunes)
		blocked := detectBlockedReason(resp.StatusCode, body)
		weak := weakContentReason(text, body, opt.MinTextChars)
		if blocked == "" {
			blocked = weak
		}
		ok := resp.StatusCode >= 200 && resp.StatusCode < 300 && weak == "" && !isHardBlocked(blocked)
		attemptInfo := Attempt{Method: "direct", StatusCode: resp.StatusCode, FinalURL: resp.Request.URL.String(), TextLen: utf8.RuneCountInString(text), BlockedReason: blocked}
		result.Attempts = append(result.Attempts, attemptInfo)
		result.FinalURL = resp.Request.URL.String()
		result.Title = title
		result.Text = text
		result.StatusCode = resp.StatusCode
		result.BlockedReason = blocked
		if ok {
			result.Success = true
			return result
		}
		if attempt < opt.Retries && retryableStatus(resp.StatusCode) {
			sleepBackoff(ctx, opt.Backoff, attempt, resp.Header.Get("Retry-After"))
			continue
		}
		return result
	}
	return result
}

func fetchJina(ctx context.Context, rawURL string, opt Options) Result {
	readerURL := strings.TrimRight(opt.JinaBaseURL, "/") + "/" + rawURL
	result := Result{URL: rawURL, Method: "jina", FinalURL: rawURL}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, readerURL, nil)
	if err != nil {
		result.Attempts = append(result.Attempts, Attempt{Method: "jina", Err: err.Error()})
		return result
	}
	req.Header.Set("User-Agent", opt.UserAgent)
	req.Header.Set("Accept", "text/markdown,text/plain;q=0.9,*/*;q=0.5")
	req.Header.Set("X-Return-Format", "text")
	req.Header.Set("X-Timeout", strconv.Itoa(int(opt.JinaTimeout/time.Second)))
	req.Header.Set("X-Retain-Images", "none")
	if key := strings.TrimSpace(os.Getenv("JINA_API_KEY")); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	cli := &http.Client{Timeout: opt.JinaTimeout}
	resp, err := cli.Do(req)
	if err != nil {
		result.Attempts = append(result.Attempts, Attempt{Method: "jina", Err: err.Error()})
		return result
	}
	body, readErr := readLimited(resp.Body, opt.MaxBodyBytes)
	_ = resp.Body.Close()
	if readErr != nil {
		result.Attempts = append(result.Attempts, Attempt{Method: "jina", StatusCode: resp.StatusCode, FinalURL: readerURL, Err: readErr.Error()})
		return result
	}
	text := markdownToText(string(body))
	text = trimRunes(text, opt.MaxTextRunes)
	blocked := detectBlockedReason(resp.StatusCode, body)
	weak := weakContentReason(text, []byte(text), opt.MinTextChars)
	if blocked == "" {
		blocked = weak
	}
	result.Attempts = append(result.Attempts, Attempt{Method: "jina", StatusCode: resp.StatusCode, FinalURL: readerURL, TextLen: utf8.RuneCountInString(text), BlockedReason: blocked})
	result.Text = text
	result.StatusCode = resp.StatusCode
	result.BlockedReason = blocked
	result.Title = firstMarkdownTitle(string(body))
	result.Success = resp.StatusCode >= 200 && resp.StatusCode < 300 && weak == "" && !isHardBlocked(blocked)
	return result
}

func setBrowserHeaders(req *http.Request, userAgent string) {
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.7")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Dnt", "1")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
}

func readLimited(r io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = 2 * 1024 * 1024
	}
	lim := io.LimitReader(r, maxBytes+1)
	b, err := io.ReadAll(lim)
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > maxBytes {
		return b[:maxBytes], nil
	}
	return b, nil
}

func extractReadableText(body []byte) (string, string) {
	if len(bytes.TrimSpace(body)) == 0 {
		return "", ""
	}
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err == nil {
		doc.Find("script,style,noscript,svg,form,nav,footer,header,aside").Remove()
		title := cleanText(doc.Find("title").First().Text())
		selection := doc.Find("article")
		if selection.Length() == 0 {
			selection = doc.Find("main")
		}
		if selection.Length() == 0 {
			selection = doc.Find("body")
		}
		if selection.Length() == 0 {
			selection = doc.Selection
		}
		return title, cleanText(selection.Text())
	}
	s := string(body)
	title := ""
	if m := regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`).FindStringSubmatch(s); len(m) == 2 {
		title = cleanText(m[1])
	}
	s = reHTMLNoise.ReplaceAllString(s, " ")
	s = reHTMLTag.ReplaceAllString(s, " ")
	return title, cleanText(s)
}

func markdownToText(s string) string {
	if idx := strings.Index(s, "\n\n---\n"); idx > 0 {
		s = s[:idx]
	}
	s = reMDLink.ReplaceAllString(s, "$1")
	s = regexp.MustCompile(`(?m)^#{1,6}\s+`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`\*{1,3}([^*]+)\*{1,3}`).ReplaceAllString(s, "$1")
	s = regexp.MustCompile("`{1,3}([^`]+)`{1,3}").ReplaceAllString(s, "$1")
	return cleanText(s)
}

func cleanText(s string) string {
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = reWS.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func trimRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:max]))
}

func detectBlockedReason(status int, body []byte) string {
	switch status {
	case http.StatusUnauthorized:
		return "auth_required"
	case http.StatusForbidden:
		return "http_403"
	case http.StatusProxyAuthRequired:
		return "proxy_auth_required"
	case http.StatusTooManyRequests:
		return "rate_limited"
	case http.StatusUnavailableForLegalReasons:
		return "legal_restriction"
	}
	lower := strings.ToLower(string(body))
	patterns := map[string]string{
		"captcha":                  "captcha",
		"verify you are human":     "human_verification",
		"are you a robot":          "human_verification",
		"human verification":       "human_verification",
		"access denied":            "access_denied",
		"request blocked":          "access_denied",
		"security check":           "security_check",
		"cloudflare":               "cloudflare_challenge",
		"cf-chl":                   "cloudflare_challenge",
		"akamai":                   "akamai_challenge",
		"perimeterx":               "bot_challenge",
		"datadome":                 "bot_challenge",
		"incapsula":                "bot_challenge",
		"too many requests":        "rate_limited",
		"rate limit":               "rate_limited",
		"login required":           "login_required",
		"please sign in":           "login_required",
		"paywall":                  "paywall",
		"\u9a8c\u8bc1\u7801":       "captcha",
		"\u4eba\u673a\u9a8c\u8bc1": "human_verification",
		"\u8bf7\u5b8c\u6210\u5b89\u5168\u9a8c\u8bc1": "security_check",
		"\u767b\u5f55\u540e\u67e5\u770b":             "login_required",
		"\u8bf7\u5148\u767b\u5f55":                   "login_required",
		"\u8bbf\u95ee\u8fc7\u4e8e\u9891\u7e41":       "rate_limited",
		"\u8bf7\u6c42\u8fc7\u4e8e\u9891\u7e41":       "rate_limited",
	}
	for pattern, reason := range patterns {
		if strings.Contains(lower, pattern) {
			return reason
		}
	}
	return ""
}

func weakContentReason(text string, body []byte, minChars int) string {
	lowerText := strings.ToLower(text)
	lowerBody := strings.ToLower(string(body))
	jsIndicators := []string{
		"please enable javascript",
		"you need to enable javascript",
		"enable javascript in your browser",
		"app__view",
		"js-support",
		"<noscript",
		"\u8bf7\u542f\u7528javascript",
		"\u8bf7\u5f00\u542fjavascript",
		"\u9700\u8981\u5141\u8bb8\u8be5\u7f51\u7ad9\u6267\u884cjavascript",
	}
	for _, marker := range jsIndicators {
		if strings.Contains(lowerText, marker) || strings.Contains(lowerBody, marker) {
			return "js_required"
		}
	}
	if utf8.RuneCountInString(text) < minChars {
		return "short_content"
	}
	return ""
}

func shouldTryJina(result Result) bool {
	if result.Success {
		return false
	}
	switch result.BlockedReason {
	case "", "short_content", "js_required":
		return true
	case "auth_required", "http_403", "proxy_auth_required", "rate_limited", "legal_restriction", "captcha", "human_verification", "access_denied", "security_check", "cloudflare_challenge", "akamai_challenge", "bot_challenge", "login_required", "paywall", "robots_denied":
		return false
	default:
		return false
	}
}

func isHardBlocked(reason string) bool {
	return reason != "" && reason != "short_content" && reason != "js_required"
}

func retryableStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func sleepBackoff(ctx context.Context, base time.Duration, attempt int, retryAfter string) {
	if retryAfter != "" {
		if seconds, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && seconds > 0 {
			select {
			case <-time.After(time.Duration(seconds) * time.Second):
			case <-ctx.Done():
			}
			return
		}
	}
	wait := time.Duration(attempt+1) * base
	select {
	case <-time.After(wait):
	case <-ctx.Done():
	}
}

func validateURL(rawURL string, allowPrivate bool) error {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("invalid url")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("unsupported url scheme")
	}
	if allowPrivate {
		return nil
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".local") {
		return fmt.Errorf("url host not allowed")
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return fmt.Errorf("url host not allowed")
		}
	}
	return nil
}

func robotsAllowed(ctx context.Context, rawURL string, opt Options) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return true
	}
	robotsURL := parsed.Scheme + "://" + parsed.Host + "/robots.txt"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, robotsURL, nil)
	if err != nil {
		return true
	}
	req.Header.Set("User-Agent", opt.UserAgent)
	cli := &http.Client{Timeout: opt.Timeout}
	resp, err := cli.Do(req)
	if err != nil {
		return true
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return true
	}
	body, err := readLimited(resp.Body, 256*1024)
	if err != nil {
		return true
	}
	return parseRobotsAllows(string(body), parsed.EscapedPath(), opt.UserAgent)
}

func parseRobotsAllows(body, requestPath, userAgent string) bool {
	type rule struct {
		allow bool
		path  string
	}
	var rules []rule
	active := false
	sawDirective := false
	uaLower := strings.ToLower(userAgent)
	for _, rawLine := range strings.Split(body, "\n") {
		line := strings.TrimSpace(rawLine)
		if idx := strings.Index(line, "#"); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
		}
		if line == "" {
			active = false
			sawDirective = false
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(parts[0]))
		value := strings.TrimSpace(parts[1])
		switch key {
		case "user-agent":
			if sawDirective {
				active = false
				sawDirective = false
			}
			v := strings.ToLower(value)
			if v == "*" || strings.Contains(uaLower, v) {
				active = true
			}
		case "allow", "disallow":
			if !active {
				continue
			}
			sawDirective = true
			if value == "" {
				continue
			}
			rules = append(rules, rule{allow: key == "allow", path: value})
		}
	}
	if requestPath == "" {
		requestPath = "/"
	}
	bestLen := -1
	allowed := true
	for _, r := range rules {
		if strings.HasPrefix(requestPath, r.path) && len(r.path) > bestLen {
			bestLen = len(r.path)
			allowed = r.allow
		}
	}
	return allowed
}

func firstMarkdownTitle(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
		if strings.HasPrefix(strings.ToLower(line), "title:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Title:"))
		}
	}
	return ""
}

func bestResult(a, b Result) Result {
	if utf8.RuneCountInString(b.Text) > utf8.RuneCountInString(a.Text) {
		return b
	}
	return a
}
