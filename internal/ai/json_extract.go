package ai

import (
	"encoding/json"
	"fmt"
	"strings"
)

// extractJSON 从 LLM 响应中提取第一个合法 JSON 数组或对象。
// 业界常见做法：模型输出可能夹杂 markdown 围栏、解释文字，只取首个 [ 或 { 到其匹配闭括号的子串。
func extractJSON(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("empty AI response")
	}

	// 快速检查：如果整段就是合法 JSON，直接返回
	if json.Valid([]byte(s)) {
		return s, nil
	}

	// 去掉常见 markdown 围栏
	s = stripMarkdownFence(s)
	if json.Valid([]byte(s)) {
		return s, nil
	}

	// 扫描寻找第一个 [ 或 { 并定位匹配闭括号
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '[' || ch == '{' {
			end := findMatchingClose(s, i)
			if end < 0 {
				continue
			}
			candidate := s[i : end+1]
			if json.Valid([]byte(candidate)) {
				return candidate, nil
			}
		}
	}

	return "", fmt.Errorf("no valid JSON found in AI response (len=%d)", len(raw))
}

func stripMarkdownFence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		// 去首行围栏（可能是 ```json / ```JSON / ``` 等）
		idx := strings.Index(s, "\n")
		if idx >= 0 {
			s = s[idx+1:]
		} else {
			s = strings.TrimPrefix(s, "```")
		}
	}
	if strings.HasSuffix(s, "```") {
		s = s[:len(s)-3]
	}
	return strings.TrimSpace(s)
}

// findMatchingClose 在 s[start] 是 [ 或 { 的前提下，找到匹配的 ] 或 }。
// 考虑字符串内的转义与嵌套。返回 -1 表示未找到。
func findMatchingClose(s string, start int) int {
	open := s[start]
	var close byte
	if open == '[' {
		close = ']'
	} else {
		close = '}'
	}
	depth := 0
	inStr := false
	for i := start; i < len(s); i++ {
		ch := s[i]
		if inStr {
			if ch == '\\' {
				i++ // 跳过转义字符
				continue
			}
			if ch == '"' {
				inStr = false
			}
			continue
		}
		switch ch {
		case '"':
			inStr = true
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// unmarshalFromLLM 从 LLM 原始响应中提取 JSON 并解析到目标结构。
// 统一替代所有手动 TrimPrefix + json.Unmarshal 的地方。
func unmarshalFromLLM[T any](raw string) (T, error) {
	var zero T
	jsonStr, err := extractJSON(raw)
	if err != nil {
		return zero, fmt.Errorf("extractJSON: %w\nraw response (first 500 chars): %.500s", err, raw)
	}
	var result T
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return zero, fmt.Errorf("json.Unmarshal: %w\njson fragment (first 500 chars): %.500s", err, jsonStr)
	}
	return result, nil
}
