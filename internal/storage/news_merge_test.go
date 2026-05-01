package storage

import (
	"testing"
	"time"

	"github.com/trendradar/backend-go/pkg/model"
)

func TestMergeCrossPlatform_SameTitle(t *testing.T) {
	results := map[string][]model.NewsItem{
		"weibo":   {{Title: "地震最新消息", URL: "http://a.cn/1", Rank: 1}},
		"baidu":   {{Title: "地震最新消息", URL: "http://b.cn/1", Rank: 3}},
		"toutiao": {{Title: "地震最新消息", URL: "http://c.cn/1", Rank: 2}},
	}
	idToName := map[string]string{
		"weibo": "微博", "baidu": "百度", "toutiao": "头条",
	}
	merged := MergeCrossPlatform(results, idToName, 0.6)
	if len(merged) != 1 {
		t.Fatalf("expected 1 merged item, got %d", len(merged))
	}
	if merged[0].TotalCount != 3 {
		t.Fatalf("expected 3 sources, got %d", merged[0].TotalCount)
	}
	if merged[0].MaxRank != 1 {
		t.Fatalf("expected maxRank=1, got %d", merged[0].MaxRank)
	}
}

func TestMergeCrossPlatform_SimilarTitle(t *testing.T) {
	results := map[string][]model.NewsItem{
		"weibo": {{Title: "某地发生6.2级地震", URL: "http://a.cn/1", Rank: 2}},
		"baidu": {{Title: "某地发生6.2级地震！", URL: "http://b.cn/1", Rank: 5}},
	}
	idToName := map[string]string{
		"weibo": "微博", "baidu": "百度",
	}
	merged := MergeCrossPlatform(results, idToName, 0.6)
	if len(merged) != 1 {
		t.Fatalf("expected 1 merged item, got %d", len(merged))
	}
}

func TestMergeCrossPlatform_DifferentTitles(t *testing.T) {
	results := map[string][]model.NewsItem{
		"weibo": {{Title: "今天是晴天", URL: "http://a.cn/1", Rank: 1}},
		"baidu": {{Title: "拜登访问火星计划启动", URL: "http://b.cn/1", Rank: 2}},
	}
	idToName := map[string]string{
		"weibo": "微博", "baidu": "百度",
	}
	merged := MergeCrossPlatform(results, idToName, 0.6)
	if len(merged) != 2 {
		t.Fatalf("expected 2 merged items, got %d", len(merged))
	}
}

func TestMergeCrossPlatform_Empty(t *testing.T) {
	merged := MergeCrossPlatform(map[string][]model.NewsItem{}, map[string]string{}, 0.6)
	if merged != nil {
		t.Fatalf("expected nil for empty input")
	}
}

func TestMergeCrossPlatform_RankOrder(t *testing.T) {
	results := map[string][]model.NewsItem{
		"toutiao": {{Title: "重要新闻A", URL: "http://c.cn/1", Rank: 10, CrawlTime: time.Now()}},
		"weibo":   {{Title: "重要新闻B", URL: "http://a.cn/1", Rank: 1, CrawlTime: time.Now()}},
		"baidu":   {{Title: "重要新闻C", URL: "http://b.cn/1", Rank: 5, CrawlTime: time.Now()}},
	}
	idToName := map[string]string{
		"weibo": "微博", "baidu": "百度", "toutiao": "头条",
	}
	merged := MergeCrossPlatform(results, idToName, 0.6)
	if len(merged) != 3 {
		t.Fatalf("expected 3, got %d", len(merged))
	}
	if merged[0].MaxRank != 1 || merged[1].MaxRank != 5 || merged[2].MaxRank != 10 {
		t.Fatalf("expected sorted by rank: %d, %d, %d", merged[0].MaxRank, merged[1].MaxRank, merged[2].MaxRank)
	}
}

func TestMergeCrossPlatform_MergeKeepsAllSourceInfo(t *testing.T) {
	results := map[string][]model.NewsItem{
		"weibo":   {{Title: "高考分数线公布", URL: "http://a.cn/1", Rank: 3}},
		"baidu":   {{Title: "高考分数线公布", URL: "http://b.cn/1", Rank: 1}},
	}
	idToName := map[string]string{
		"weibo": "微博", "baidu": "百度",
	}
	merged := MergeCrossPlatform(results, idToName, 0.6)
	if len(merged[0].Sources) != 2 {
		t.Fatalf("expected 2 sources, got %d", len(merged[0].Sources))
	}
	// 排名最高的来源应排前面（rank 小的）
	if merged[0].Sources[0].SourceID != "baidu" {
		t.Fatalf("expected baidu first (rank 1), got %s", merged[0].Sources[0].SourceID)
	}
}
