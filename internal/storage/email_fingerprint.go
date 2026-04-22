package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/trendradar/backend-go/pkg/model"
)

// EmailItemSignature 写入 DB 的指纹，采用规范化 URL + 归一化标题 的新版算法
func EmailItemSignature(platformID string, item model.NewsItem) string {
	primary, _ := itemFingerprints(platformID, item)
	return primary
}

// matchSigs 用于 “是否已发” 的查询；包含新版与历史版本，避免升级后同一条因算法差异重复推送
func matchSigs(platformID string, item model.NewsItem) []string {
	_, all := itemFingerprints(platformID, item)
	seen := make(map[string]struct{}, len(all))
	out := make([]string, 0, len(all))
	for _, s := range all {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// itemFingerprints 返回 (主指纹, 所有匹配用指纹) — 主指纹用于新写入
func itemFingerprints(platformID string, item model.NewsItem) (primary string, all []string) {
	raw := strings.TrimSpace(item.URL)
	if raw == "" {
		raw = strings.TrimSpace(item.MobileURL)
	}
	if raw != "" {
		return urlFingerprints(raw)
	}
	nt := normalizeTitleForFingerprint(item.Title)
	tt := strings.TrimSpace(item.Title)
	primary = sha256hex(platformID + "\x00" + nt)
	legacy := sha256hex(platformID + "\x00" + tt)
	if legacy != primary {
		return primary, []string{primary, legacy}
	}
	return primary, []string{primary}
}

func urlFingerprints(raw string) (primary string, all []string) {
	raw = strings.TrimSpace(raw)
	legacy := sha256hex(strings.ToLower(raw))

	canon := normalizeURLString(raw)
	if canon == "" {
		return legacy, []string{legacy}
	}
	primary = sha256hex(canon)
	if primary == legacy {
		return primary, []string{primary}
	}
	// 库中可能仅存旧版「整串小写」指纹，查询时需带 legacy
	return primary, []string{primary, legacy}
}

// normalizeURLString 将 URL 规范化为可稳定比较的形式（与业界常见去重/爬虫做法一致：去跟踪参数、稳定 query 序等）
func normalizeURLString(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		if strings.HasPrefix(raw, "//") {
			raw = "https:" + raw
		} else {
			// 相对或畸形链接：不做结构化规范化，让上层用 legacy
			return ""
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if u.Hostname() == "" {
		return ""
	}
	sch := strings.ToLower(strings.TrimSpace(u.Scheme))
	if sch != "http" && sch != "https" {
		return ""
	}
	u.Scheme = sch
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		u.Host = host
	} else if (sch == "https" && port == "443") || (sch == "http" && port == "80") {
		u.Host = host
	} else {
		u.Host = net.JoinHostPort(host, port)
	}
	u.Fragment = ""
	if u.Path == "" || u.Path == "." {
		u.Path = "/"
	} else {
		u.Path = path.Clean(u.Path)
	}
	q := u.Query()
	if len(q) == 0 {
		return u.String()
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		if isTrackingQueryKey(k) {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(url.Values)
	for _, k := range keys {
		vs := q[k]
		// 稳定：同 key 多值时排序
		cp := make([]string, len(vs))
		copy(cp, vs)
		sort.Strings(cp)
		out[k] = cp
	}
	u.RawQuery = out.Encode()
	return u.String()
}

func isTrackingQueryKey(k string) bool {
	kl := strings.ToLower(strings.TrimSpace(k))
	if strings.HasPrefix(kl, "utm_") {
		return true
	}
	switch kl {
	case "gclid", "gclsrc", "dclid", "fbclid", "msclkid", "mc_cid", "mc_eid",
		"igshid", "si", "spm", "scm", "ref", "from", "source", "cmpid", "mkt_tok", "_ga", "_gl", "w_rid":
		return true
	default:
		return false
	}
}

// normalizeTitleForFingerprint 无 URL 时的标题：折叠空白、统一空格，略降标题微调导致的“假新”
func normalizeTitleForFingerprint(title string) string {
	s := strings.TrimSpace(title)
	if s == "" {
		return ""
	}
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return strings.Join(fields, " ")
}

func sha256hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
