package webfetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testOptions() Options {
	return Options{
		Timeout:           2 * time.Second,
		Retries:           0,
		Backoff:           time.Millisecond,
		MinTextChars:      40,
		MaxTextRunes:      1000,
		MaxBodyBytes:      1024 * 1024,
		UserAgent:         defaultUserAgent,
		JinaEnabled:       false,
		JinaTimeout:       2 * time.Second,
		JinaBaseURL:       defaultReaderURL,
		AllowPrivateHosts: true,
	}
}

func TestFetchWithOptionsDirectArticle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); ua == "" {
			t.Fatalf("missing user agent")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><head><title>Example</title></head><body><nav>menu</nav><article><h1>Hello</h1><p>This is a readable article body with enough text for extraction.</p></article><script>bad()</script></body></html>`))
	}))
	defer server.Close()

	result := FetchWithOptions(context.Background(), server.URL, testOptions())
	if !result.Success {
		t.Fatalf("expected success, got %+v err=%v", result, result.Error())
	}
	if result.Method != "direct" {
		t.Fatalf("expected direct method, got %s", result.Method)
	}
	if strings.Contains(result.Text, "menu") || strings.Contains(result.Text, "bad") {
		t.Fatalf("expected boilerplate/script removed, got %q", result.Text)
	}
	if !strings.Contains(result.Text, "readable article body") {
		t.Fatalf("missing article text: %q", result.Text)
	}
}

func TestFetchWithOptionsUsesReaderForJSShell(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body><noscript>Please enable JavaScript</noscript><div id="app"></div></body></html>`))
	}))
	defer page.Close()

	reader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`# Rendered Title

Reader returned a rendered article body with enough useful text after JavaScript rendering.`))
	}))
	defer reader.Close()

	opt := testOptions()
	opt.JinaEnabled = true
	opt.JinaBaseURL = reader.URL
	result := FetchWithOptions(context.Background(), page.URL, opt)
	if !result.Success {
		t.Fatalf("expected reader success, got %+v err=%v", result, result.Error())
	}
	if result.Method != "jina" {
		t.Fatalf("expected jina method, got %s", result.Method)
	}
	if !strings.Contains(result.Text, "rendered article body") {
		t.Fatalf("missing reader text: %q", result.Text)
	}
}

func TestFetchWithOptionsDoesNotBypassHardBlock(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`captcha required: verify you are human`))
	}))
	defer page.Close()

	readerCalled := false
	reader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readerCalled = true
		_, _ = w.Write([]byte(`should not be used`))
	}))
	defer reader.Close()

	opt := testOptions()
	opt.JinaEnabled = true
	opt.JinaBaseURL = reader.URL
	result := FetchWithOptions(context.Background(), page.URL, opt)
	if result.Success {
		t.Fatalf("expected hard block failure, got %+v", result)
	}
	if readerCalled {
		t.Fatalf("reader should not be called for hard blocked pages")
	}
	if result.BlockedReason == "" {
		t.Fatalf("expected blocked reason, got %+v", result)
	}
}
