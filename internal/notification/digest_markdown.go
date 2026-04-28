package notification

import (
	"fmt"
	"strings"
	"time"

	"github.com/trendradar/backend-go/pkg/model"
)

// BuildNewsDigestMarkdown 生成与邮件摘要同构的 **Markdown** 正文，供 Server 酱（微信方糖）desp 使用
func BuildNewsDigestMarkdown(
	generatedAt time.Time,
	platformCount, afterAICount, mailCount, emailSkipped, rssTotal int,
	failedIDs []string,
	filterMode string,
	results map[string][]model.NewsItem,
	idToName map[string]string,
	crawlTime time.Time,
) string {
	var b strings.Builder
	b.WriteString("# 趋势雷达 · 移动端行业快报\n\n")
	b.WriteString("## 执行摘要\n\n")
	b.WriteString(fmt.Sprintf("- **时间**: `%s`\n", generatedAt.Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("- **平台数**: %d\n", platformCount))
	b.WriteString(fmt.Sprintf("- **关注新闻（过滤后）**: %d 条\n", afterAICount))
	b.WriteString(fmt.Sprintf("- **本批新推送**: %d 条\n", mailCount))
	if emailSkipped > 0 {
		b.WriteString(fmt.Sprintf("- **去重说明**: 已排除历史已发送 %d 条\n", emailSkipped))
	}
	b.WriteString(fmt.Sprintf("- **RSS 条目**: %d\n", rssTotal))
	failStr := "无"
	if len(failedIDs) > 0 {
		failStr = strings.Join(failedIDs, "、")
	}
	b.WriteString(fmt.Sprintf("- **失败平台**: %s\n", failStr))
	b.WriteString(fmt.Sprintf("- **策略**: %s\n", mdEscapeLine(filterMode)))

	b.WriteString("\n## 平台覆盖\n\n")
	b.WriteString(mdFormatPlatformCoverage(results, idToName))
	b.WriteString("\n\n## 重点新闻 TOP\n\n")
	b.WriteString(mdFormatNewsBrief(results, idToName, crawlTime))
	return b.String()
}

func mdEscapeLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 500 {
		s = s[:500] + "…"
	}
	return s
}

func mdFormatPlatformCoverage(results map[string][]model.NewsItem, idToName map[string]string) string {
	if len(results) == 0 {
		return "_（无）_"
	}
	var b strings.Builder
	for platformID, items := range results {
		name := idToName[platformID]
		if strings.TrimSpace(name) == "" {
			name = platformID
		}
		b.WriteString(fmt.Sprintf("- **%s**：%d 条\n", mdEscapeInline(name), len(items)))
	}
	return strings.TrimSpace(b.String())
}

func mdFormatNewsBrief(results map[string][]model.NewsItem, idToName map[string]string, fallback time.Time) string {
	if len(results) == 0 {
		return "_暂无重点新闻_"
	}
	const maxPerPlatform = 5
	var b strings.Builder
	for platformID, items := range results {
		if len(items) == 0 {
			continue
		}
		name := idToName[platformID]
		if strings.TrimSpace(name) == "" {
			name = platformID
		}
		b.WriteString(fmt.Sprintf("### %s\n\n", mdEscapeInline(name)))
		limit := len(items)
		if limit > maxPerPlatform {
			limit = maxPerPlatform
		}
		for i := 0; i < limit; i++ {
			item := items[i]
			title := strings.TrimSpace(item.Title)
			rn := []rune(title)
			if len(rn) > 80 {
				title = string(rn[:80]) + "…"
			}
			link := strings.TrimSpace(item.URL)
			if link == "" {
				link = strings.TrimSpace(item.MobileURL)
			}
			if link == "" {
				b.WriteString(fmt.Sprintf("%d. %s  \n", i+1, mdEscapeInline(title)))
				b.WriteString("   _无链接_\n\n")
				continue
			}
			itemTime := item.CrawlTime
			if itemTime.IsZero() {
				itemTime = fallback
			}
			// 使用「标题 + 可点击链」的 Markdown
			b.WriteString(fmt.Sprintf("%d. **%s**  \n", i+1, mdEscapeInline(title)))
			b.WriteString(fmt.Sprintf("   _%s_ · [原文](%s)\n\n", itemTime.Format("15:04"), mdEscapeURL(link)))
		}
	}
	return strings.TrimSpace(b.String())
}

func mdEscapeInline(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	// 避免破坏 ** 粗体 与链接
	s = strings.ReplaceAll(s, "**", "†")
	return s
}

// mdEscapeURL 尽量保证 URL 在 Markdown 链中可解析
func mdEscapeURL(u string) string {
	u = strings.ReplaceAll(u, " ", "%20")
	u = strings.ReplaceAll(u, ")", "%29")
	return u
}

// BuildNewsDigestMarkdownMerged 跨平台合并后的 Markdown 摘要
func BuildNewsDigestMarkdownMerged(
	generatedAt time.Time,
	platformCount, afterAICount, mailCount, mergedCount, emailSkipped, rssTotal int,
	failedIDs []string,
	filterMode string,
	merged []model.MergedNewsItem,
	crawlTime time.Time,
) string {
	var b strings.Builder
	b.WriteString("# 趋势雷达 · 移动端行业快报\n\n")
	b.WriteString("## 执行摘要\n\n")
	b.WriteString(fmt.Sprintf("- **时间**: `%s`\n", generatedAt.Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("- **平台数**: %d\n", platformCount))
	b.WriteString(fmt.Sprintf("- **关注新闻（过滤后）**: %d 条\n", afterAICount))
	b.WriteString(fmt.Sprintf("- **本批新推送**: %d 条\n", mailCount))
	if mergedCount < mailCount {
		b.WriteString(fmt.Sprintf("- **跨平台合并后**: %d 条\n", mergedCount))
	}
	if emailSkipped > 0 {
		b.WriteString(fmt.Sprintf("- **去重说明**: 已排除历史已发送 %d 条\n", emailSkipped))
	}
	b.WriteString(fmt.Sprintf("- **RSS 条目**: %d\n", rssTotal))
	failStr := "无"
	if len(failedIDs) > 0 {
		failStr = strings.Join(failedIDs, "、")
	}
	b.WriteString(fmt.Sprintf("- **失败平台**: %s\n", failStr))
	b.WriteString(fmt.Sprintf("- **策略**: %s\n", mdEscapeLine(filterMode)))

	b.WriteString("\n## 重点新闻 TOP\n\n")
	b.WriteString(mdFormatNewsBriefMerged(merged, crawlTime))
	return b.String()
}

func mdFormatNewsBriefMerged(merged []model.MergedNewsItem, fallback time.Time) string {
	if len(merged) == 0 {
		return "_暂无重点新闻_"
	}
	const maxItems = 20
	var b strings.Builder
	limit := len(merged)
	if limit > maxItems {
		limit = maxItems
	}
	for i := 0; i < limit; i++ {
		item := merged[i]
		title := strings.TrimSpace(item.Title)
		rn := []rune(title)
		if len(rn) > 80 {
			title = string(rn[:80]) + "..."
		}
		link := strings.TrimSpace(item.URL)
		if link == "" {
			link = strings.TrimSpace(item.MobileURL)
		}
		itemTime := item.CrawlTime
		if itemTime.IsZero() {
			itemTime = fallback
		}
		// 来源标签
		var sourceParts []string
		for _, src := range item.Sources {
			sourceParts = append(sourceParts, fmt.Sprintf("%s #%d", mdEscapeInline(src.SourceName), src.Rank))
		}
		sourceTag := strings.Join(sourceParts, " · ")
		if link == "" {
			b.WriteString(fmt.Sprintf("%d. **%s**  \n", i+1, mdEscapeInline(title)))
			b.WriteString(fmt.Sprintf("   _%s_ · _%s_\n\n", sourceTag, itemTime.Format("15:04")))
			continue
		}
		b.WriteString(fmt.Sprintf("%d. **%s**  \n", i+1, mdEscapeInline(title)))
		b.WriteString(fmt.Sprintf("   _%s_ · _%s_ · [原文](%s)\n\n", sourceTag, itemTime.Format("15:04"), mdEscapeURL(link)))
	}
	return strings.TrimSpace(b.String())
}
