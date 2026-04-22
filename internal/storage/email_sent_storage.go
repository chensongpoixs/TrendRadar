package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/trendradar/backend-go/internal/core"
	"github.com/trendradar/backend-go/pkg/model"
	"gorm.io/gorm/clause"
)

// EmailItemSignature 生成邮件去重签名：优先稳定 URL，否则 平台+标题
func EmailItemSignature(platformID string, item model.NewsItem) string {
	u := strings.TrimSpace(item.URL)
	if u == "" {
		u = strings.TrimSpace(item.MobileURL)
	}
	if u != "" {
		return sha256hex(strings.ToLower(u))
	}
	raw := platformID + "\x00" + strings.TrimSpace(item.Title)
	return sha256hex(raw)
}

func sha256hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// FilterNotYetEmailed 筛掉历史已发邮件中出现过的热榜条目，返回新条目与跳过数量
func FilterNotYetEmailed(results map[string][]model.NewsItem) (map[string][]model.NewsItem, int, error) {
	if len(results) == 0 {
		return results, 0, nil
	}
	type pair struct {
		platformID string
		item       model.NewsItem
		sig        string
	}
	var pairs []pair
	sigs := make([]string, 0)
	for pid, items := range results {
		for _, item := range items {
			sig := EmailItemSignature(pid, item)
			pairs = append(pairs, pair{platformID: pid, item: item, sig: sig})
			sigs = append(sigs, sig)
		}
	}
	if len(sigs) == 0 {
		return map[string][]model.NewsItem{}, 0, nil
	}
	db := core.GetDB()
	if db == nil {
		// 无库则不去重
		return results, 0, nil
	}
	var existing []model.EmailSentFingerprint
	if err := db.Where("sig IN ?", sigs).Find(&existing).Error; err != nil {
		return nil, 0, err
	}
	existSet := make(map[string]bool, len(existing))
	for _, e := range existing {
		existSet[e.Sig] = true
	}
	kept := make(map[string][]model.NewsItem)
	skipped := 0
	for _, p := range pairs {
		if existSet[p.sig] {
			skipped++
			continue
		}
		kept[p.platformID] = append(kept[p.platformID], p.item)
	}
	return kept, skipped, nil
}

// RecordEmailSent 邮件发送成功后记录指纹（可重复调用，冲突忽略）
func RecordEmailSent(results map[string][]model.NewsItem) error {
	db := core.GetDB()
	if db == nil {
		return nil
	}
	now := time.Now()
	var rows []model.EmailSentFingerprint
	for pid, items := range results {
		for _, item := range items {
			rows = append(rows, model.EmailSentFingerprint{
				Sig:         EmailItemSignature(pid, item),
				FirstSentAt: now,
			})
		}
	}
	if len(rows) == 0 {
		return nil
	}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "sig"}},
		DoNothing: true,
	}).CreateInBatches(&rows, 200).Error
}
