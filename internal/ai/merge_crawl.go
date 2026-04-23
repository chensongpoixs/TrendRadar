package ai

import (
	"sort"

	"github.com/trendradar/backend-go/pkg/model"
)

// MergeHotlistByRank 将多个按平台分组的条目标合并，按 rank 升序；rank=0 视为脱榜/尾排。
func MergeHotlistByRank(parts ...map[string][]model.NewsItem) map[string][]model.NewsItem {
	keys := make(map[string]struct{})
	for _, p := range parts {
		for k := range p {
			keys[k] = struct{}{}
		}
	}
	out := make(map[string][]model.NewsItem, len(keys))
	for k := range keys {
		var block []model.NewsItem
		for _, p := range parts {
			block = append(block, p[k]...)
		}
		if len(block) == 0 {
			continue
		}
		sort.Slice(block, func(i, j int) bool {
			ri, rj := block[i].Rank, block[j].Rank
			if ri == 0 && rj == 0 {
				return block[i].Title < block[j].Title
			}
			if ri == 0 {
				return false
			}
			if rj == 0 {
				return true
			}
			if ri != rj {
				return ri < rj
			}
			return block[i].Title < block[j].Title
		})
		out[k] = block
	}
	return out
}
