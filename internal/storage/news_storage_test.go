package storage

import (
	"testing"
)

// ==================== NormalizeURL 测试 ====================

func TestNormalizeURL_Basic(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "空 URL",
			input:    "",
			expected: "",
		},
		{
			name:     "基本 URL",
			input:    "https://example.com/article/123",
			expected: "https://example.com/article/123",
		},
		{
			name:     "尾部斜杠移除",
			input:    "https://example.com/article/123/",
			expected: "https://example.com/article/123",
		},
		{
			name:     "根路径保留斜杠",
			input:    "https://example.com/",
			expected: "https://example.com",
		},
		{
			name:     "大小写统一",
			input:    "HTTPS://EXAMPLE.COM/Article/123",
			expected: "https://example.com/article/123",
		},
		{
			name:     "移除片段标识符",
			input:    "https://example.com/article#section",
			expected: "https://example.com/article",
		},
		{
			name:     "前后空白移除",
			input:    "  https://example.com/article  ",
			expected: "https://example.com/article",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NormalizeURL(tt.input)
			if result != tt.expected {
				t.Errorf("NormalizeURL(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestNormalizeURL_TrackingParams(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "移除 UTM 参数",
			input:    "https://example.com/article?utm_source=weibo&utm_campaign=test",
			expected: "https://example.com/article",
		},
		{
			name:     "保留非跟踪参数",
			input:    "https://example.com/article?param1=value1&utm_source=weibo",
			expected: "https://example.com/article?param1=value1",
		},
		{
			name:     "移除 share 参数",
			input:    "https://example.com/article?share_id=12345&share_token=abc",
			expected: "https://example.com/article",
		},
		{
			name:     "保留多个非跟踪参数",
			input:    "https://example.com/article?sort=date&order=desc&utm_source=weibo",
			expected: "https://example.com/article?sort=date&order=desc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NormalizeURL(tt.input)
			if result != tt.expected {
				t.Errorf("NormalizeURL(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestNormalizeURL_DuplicateSlashes(t *testing.T) {
	input := "https://example.com//article///123"
	result := NormalizeURL(input)
	expected := "https://example.com/article/123"
	if result != expected {
		t.Errorf("NormalizeURL(%q) = %q, want %q", input, result, expected)
	}
}

// ==================== IsURLTracked 测试 ====================

func TestIsURLTracked(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		expected bool
	}{
		{
			name:     "无跟踪参数",
			url:      "https://example.com/article",
			expected: false,
		},
		{
			name:     "有 UTM 参数",
			url:      "https://example.com/article?utm_source=weibo",
			expected: true,
		},
		{
			name:     "有 share 参数",
			url:      "https://example.com/article?share_id=123",
			expected: true,
		},
		{
			name:     "有 ref 参数",
			url:      "https://example.com/article?ref=weixin",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsURLTracked(tt.url)
			if result != tt.expected {
				t.Errorf("IsURLTracked(%q) = %v, want %v", tt.url, result, tt.expected)
			}
		})
	}
}

// ==================== ExtractBaseURL 测试 ====================

func TestExtractBaseURL(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		expected string
	}{
		{
			name:     "无参数无片段",
			url:      "https://example.com/article",
			expected: "https://example.com/article",
		},
		{
			name:     "有参数",
			url:      "https://example.com/article?id=123",
			expected: "https://example.com/article",
		},
		{
			name:     "有片段",
			url:      "https://example.com/article#section",
			expected: "https://example.com/article",
		},
		{
			name:     "有参数和片段",
			url:      "https://example.com/article?id=123#section",
			expected: "https://example.com/article",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractBaseURL(tt.url)
			if result != tt.expected {
				t.Errorf("ExtractBaseURL(%q) = %q, want %q", tt.url, result, tt.expected)
			}
		})
	}
}
