package ai

import (
	"testing"

	"github.com/trendradar/backend-go/pkg/model"
)

func TestMergeHotlistByRank(t *testing.T) {
	a := map[string][]model.NewsItem{
		"p": {{Rank: 2, Title: "b"}, {Rank: 1, Title: "a"}},
	}
	b := map[string][]model.NewsItem{
		"p": {{Rank: 3, Title: "c"}},
	}
	m := MergeHotlistByRank(a, b)
	if len(m["p"]) != 3 {
		t.Fatalf("len=%d", len(m["p"]))
	}
	if m["p"][0].Title != "a" || m["p"][2].Title != "c" {
		t.Fatalf("order: %#v", m["p"])
	}
}
