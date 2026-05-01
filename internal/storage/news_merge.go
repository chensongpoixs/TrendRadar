package storage

import (
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/trendradar/backend-go/pkg/model"
)

// MergeCrossPlatform 跨平台合并相同/高度相似的新闻标题。
// threshold 为 Jaccard 相似度阈值（0~1），默认 0.6。
// 合并后按 MaxRank 升序排列。
func MergeCrossPlatform(results map[string][]model.NewsItem, idToName map[string]string, threshold float64) []model.MergedNewsItem {
	if threshold <= 0 {
		threshold = 0.6
	}

	// 1. 展平所有平台新闻为带来源信息的中间结构
	type flatItem struct {
		title    string
		url      string
		mobileURL string
		sourceID string
		rank     int
		crawlTime time.Time
	}
	var flat []flatItem
	for platformID, items := range results {
		for _, item := range items {
			flat = append(flat, flatItem{
				title:     item.Title,
				url:       item.URL,
				mobileURL: item.MobileURL,
				sourceID:  platformID,
				rank:      item.Rank,
				crawlTime: item.CrawlTime,
			})
		}
	}
	if len(flat) == 0 {
		return nil
	}

	// 2. 归一化标题并提取关键词
	type indexedItem struct {
		flatItem
		normTitle string
		keywords  []string
	}
	indexed := make([]indexedItem, len(flat))
	for i, f := range flat {
		indexed[i] = indexedItem{
			flatItem:  f,
			normTitle: normalizeTitleForMerge(f.title),
			keywords:  extractKeywordsForMerge(f.title),
		}
	}

	// 3. 贪心聚类：按标题长度排序（长标题优先作为聚类中心）
	sort.Slice(indexed, func(i, j int) bool {
		return len(indexed[i].normTitle) > len(indexed[j].normTitle)
	})

	used := make([]bool, len(indexed))
	var clusters [][]indexedItem

	for i := 0; i < len(indexed); i++ {
		if used[i] {
			continue
		}
		cluster := []indexedItem{indexed[i]}
		used[i] = true

		for j := i + 1; j < len(indexed); j++ {
			if used[j] {
				continue
			}
			sim := calculateKeywordSimilarity(indexed[i].keywords, indexed[j].keywords)
			if sim >= threshold {
				cluster = append(cluster, indexed[j])
				used[j] = true
			}
		}
		clusters = append(clusters, cluster)
	}

	// 4. 为每个聚类生成一个 MergedNewsItem
	merged := make([]model.MergedNewsItem, 0, len(clusters))
	for _, cluster := range clusters {
		item := model.MergedNewsItem{
			Title:   cluster[0].title,
			Sources: make([]model.NewsSource, 0, len(cluster)),
		}
		var latestTime time.Time

		for _, ci := range cluster {
			name := idToName[ci.sourceID]
			if name == "" {
				name = ci.sourceID
			}
			item.Sources = append(item.Sources, model.NewsSource{
				SourceID:   ci.sourceID,
				SourceName: name,
				Rank:       ci.rank,
				URL:        ci.url,
			})
			// 取最高排名（数字越小越好）
			if item.MaxRank == 0 || (ci.rank > 0 && ci.rank < item.MaxRank) {
				item.MaxRank = ci.rank
				item.URL = ci.url
				item.MobileURL = ci.mobileURL
			}
			if ci.crawlTime.After(latestTime) {
				latestTime = ci.crawlTime
			}
		}
		item.CrawlTime = latestTime
		item.TotalCount = len(cluster)

		// 排序来源：按 rank 升序
		sort.Slice(item.Sources, func(i, j int) bool {
			ri, rj := item.Sources[i].Rank, item.Sources[j].Rank
			if ri == 0 && rj == 0 {
				return item.Sources[i].SourceName < item.Sources[j].SourceName
			}
			if ri == 0 {
				return false
			}
			if rj == 0 {
				return true
			}
			return ri < rj
		})
		merged = append(merged, item)
	}

	// 5. 标题文本去重：如果聚类后标题仍高度相似（归一化后相同），合并来源
	merged = dedupByNormalizedTitle(merged)

	// 6. 按 MaxRank 升序
	sort.Slice(merged, func(i, j int) bool {
		ri, rj := merged[i].MaxRank, merged[j].MaxRank
		if ri == 0 && rj == 0 {
			return len(merged[i].Title) > len(merged[j].Title)
		}
		if ri == 0 {
			return false
		}
		if rj == 0 {
			return true
		}
		return ri < rj
	})

	return merged
}

// dedupByNormalizedTitle 归一化标题相同时合并来源
func dedupByNormalizedTitle(items []model.MergedNewsItem) []model.MergedNewsItem {
	seen := make(map[string]int) // normTitle -> index in result
	result := make([]model.MergedNewsItem, 0, len(items))

	for _, item := range items {
		norm := normalizeTitleForMerge(item.Title)
		if idx, ok := seen[norm]; ok {
			// 合并来源
			result[idx].Sources = append(result[idx].Sources, item.Sources...)
			// 更新 MaxRank
			if item.MaxRank > 0 && (result[idx].MaxRank == 0 || item.MaxRank < result[idx].MaxRank) {
				result[idx].MaxRank = item.MaxRank
				result[idx].URL = item.URL
				result[idx].MobileURL = item.MobileURL
			}
			result[idx].TotalCount += item.TotalCount
			if item.CrawlTime.After(result[idx].CrawlTime) {
				result[idx].CrawlTime = item.CrawlTime
			}
		} else {
			seen[norm] = len(result)
			result = append(result, item)
		}
	}

	// 重新排序合并后的来源
	for i := range result {
		sort.Slice(result[i].Sources, func(a, b int) bool {
			ra, rb := result[i].Sources[a].Rank, result[i].Sources[b].Rank
			if ra == 0 && rb == 0 {
				return result[i].Sources[a].SourceName < result[i].Sources[b].SourceName
			}
			if ra == 0 {
				return false
			}
			if rb == 0 {
				return true
			}
			return ra < rb
		})
	}

	return result
}

// normalizeTitleForMerge 归一化标题用于去重比较
func normalizeTitleForMerge(title string) string {
	s := strings.TrimSpace(title)
	// 转小写
	s = strings.ToLower(s)
	// 去标点，保留中文、英文、数字和空格
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) {
			b.WriteRune(r)
		} else if unicode.Is(unicode.Han, r) {
			b.WriteRune(r)
		}
	}
	s = b.String()
	// 折叠多余空格
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

// extractKeywordsForMerge 从标题提取关键词（用于相似度计算）
func extractKeywordsForMerge(title string) []string {
	sep := func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("—-|·,，、。！？：；()（）【】《》\"'", r)
	}
	words := strings.FieldsFunc(title, sep)
	keywords := make([]string, 0, len(words))
	for _, w := range words {
		w = strings.TrimSpace(strings.ToLower(w))
		if len([]rune(w)) > 1 {
			keywords = append(keywords, w)
		}
	}
	return keywords
}

// calculateKeywordSimilarity Jaccard 相似度
func calculateKeywordSimilarity(kw1, kw2 []string) float64 {
	if len(kw1) == 0 || len(kw2) == 0 {
		return 0
	}
	set1 := make(map[string]struct{}, len(kw1))
	for _, k := range kw1 {
		set1[k] = struct{}{}
	}
	set2 := make(map[string]struct{}, len(kw2))
	for _, k := range kw2 {
		set2[k] = struct{}{}
	}
	intersection := 0
	for k := range set1 {
		if _, ok := set2[k]; ok {
			intersection++
		}
	}
	union := len(set1) + len(set2) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}
