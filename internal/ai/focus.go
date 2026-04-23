package ai

import (
	"strings"

	"github.com/trendradar/backend-go/pkg/config"
	"github.com/trendradar/backend-go/pkg/logger"
	"github.com/trendradar/backend-go/pkg/model"
	"go.uber.org/zap"
)

// FocusFilterEnforced 全局是否启用「AI 兴趣过滤」（与调度器、邮件同一口径）。
// 为 true 时，任一条抓取路径不应再依赖单独 query 才过滤，且同一请求内只应调用 ApplyFocusFilter 一次。
func FocusFilterEnforced() bool {
	cfg := config.Get()
	if cfg == nil {
		return false
	}
	return strings.ToLower(strings.TrimSpace(cfg.Filter.Method)) == "ai" &&
		strings.TrimSpace(cfg.Filter.Interests) != ""
}

// ApplyFocusFilter 按 AI 兴趣描述过滤热榜条目，返回命中的子集。
// 若 AI 过滤未启用（FocusFilterEnforced 为 false）或调用失败，原样返回 results。
func ApplyFocusFilter(results map[string][]model.NewsItem) map[string][]model.NewsItem {
	if !FocusFilterEnforced() {
		return results
	}
	cfg := config.Get()
	if cfg == nil {
		return results
	}

	flat := make([]NewsItem, 0)
	type itemRef struct {
		platformID string
		item       model.NewsItem
	}
	refs := make([]itemRef, 0)
	for platformID, items := range results {
		for _, item := range items {
			flat = append(flat, NewsItem{
				Title:  item.Title,
				Rank:   item.Rank,
				Source: platformID,
			})
			refs = append(refs, itemRef{platformID: platformID, item: item})
		}
	}
	if len(flat) == 0 {
		return results
	}

	filter := NewFilter(cfg.Filter.Interests, cfg.AIFilter.MinScore, FilterOptions{
		BatchSize:       cfg.AIFilter.BatchSize,
		MaxInputChars:   cfg.AIFilter.MaxInputChars,
		BatchIntervalMS: cfg.AIFilter.BatchInterval,
		MaxOutputTokens: cfg.AIFilter.MaxOutputTokens,
	})
	allFilterResults, err := filter.FilterNews(flat)
	if err != nil {
		logger.WithComponent("aifilter").Error("focus filter failed, using original data", zap.Error(err))
		return results
	}
	minScore := cfg.AIFilter.MinScore
	if minScore <= 0 {
		minScore = 0.7
	}
	keptResults := GetFilteredItems(allFilterResults, minScore)
	logAIFocusFilterDetails(minScore, len(refs), allFilterResults, keptResults)
	filterResults := keptResults

	allowed := make(map[string]bool)
	for _, r := range filterResults {
		allowed[r.Item.Source+"::"+r.Item.Title] = true
	}

	kept := make(map[string][]model.NewsItem)
	for _, ref := range refs {
		key := ref.platformID + "::" + ref.item.Title
		if allowed[key] {
			kept[ref.platformID] = append(kept[ref.platformID], ref.item)
		}
	}
	return kept
}

func logAIFocusFilterDetails(minScore float64, inputCount int, all []FilterResult, kept []FilterResult) {
	lg := logger.WithComponent("aifilter")
	keptSet := make(map[string]struct{}, len(kept))
	for _, r := range kept {
		keptSet[r.Item.Source+"::"+r.Item.Title] = struct{}{}
	}
	dropped := inputCount - len(kept)
	if dropped < 0 {
		dropped = 0
	}
	lg.Info("aifilter summary",
		zap.Float64("min_score", minScore), zap.Int("input", inputCount),
		zap.Int("model_rows", len(all)), zap.Int("kept", len(kept)), zap.Int("dropped", dropped))
	if len(all) != inputCount {
		lg.Warn("model row count mismatch", zap.Int("model_rows", len(all)), zap.Int("input", inputCount))
	}
	for i, r := range all {
		_, pass := keptSet[r.Item.Source+"::"+r.Item.Title]
		passStr := "drop"
		if pass {
			passStr = "keep"
		}
		tags := strings.Join(r.Tags, ",")
		if tags == "" {
			tags = "-"
		}
		title := truncateRunesForLog(strings.TrimSpace(r.Item.Title), 120)
		reason := truncateRunesForLog(strings.TrimSpace(r.Reason), 200)
		src := strings.TrimSpace(r.Item.Source)
		if src == "" {
			src = "?"
		}
		lg.Debug("aifilter line",
			zap.Int("idx", i), zap.String("pass", passStr), zap.Float64("score", r.Score),
			zap.String("src", src), zap.String("title", title), zap.String("reason", reason), zap.String("tags", tags))
	}
}

func truncateRunesForLog(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
