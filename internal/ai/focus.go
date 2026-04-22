package ai

import (
	"log"
	"strings"

	"github.com/trendradar/backend-go/pkg/config"
	"github.com/trendradar/backend-go/pkg/model"
)

// ApplyFocusFilter 按 AI 兴趣描述过滤热榜条目，返回命中的子集。
// 若 AI 过滤未启用或调用失败，原样返回 results。
func ApplyFocusFilter(results map[string][]model.NewsItem) map[string][]model.NewsItem {
	cfg := config.Get()
	if cfg == nil || strings.ToLower(cfg.Filter.Method) != "ai" || strings.TrimSpace(cfg.Filter.Interests) == "" {
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

	filter := NewFilter(cfg.Filter.Interests, cfg.AIFilter.MinScore, cfg.AIFilter.BatchSize)
	filterResults, err := filter.FilterNews(flat)
	if err != nil {
		log.Printf("AI focus filter failed, fallback to original data: %v", err)
		return results
	}
	minScore := cfg.AIFilter.MinScore
	if minScore <= 0 {
		minScore = 0.7
	}
	filterResults = GetFilteredItems(filterResults, minScore)

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
	log.Printf("AI focus filter applied with min_score=%.2f: %d -> %d", minScore, len(refs), len(filterResults))
	return kept
}
