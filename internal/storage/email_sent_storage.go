package storage

import (
	"sort"
	"time"

	"github.com/trendradar/backend-go/internal/core"
	"github.com/trendradar/backend-go/pkg/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const emailDedupINChunk = 500

// FilterNotYetEmailed 筛掉历史已发邮件中出现过的热榜条目，返回新条目与跳过数量
// 跳过含：库中已存在任一同类指纹、本批内按主指纹重复（先出现者保留）
func FilterNotYetEmailed(results map[string][]model.NewsItem) (map[string][]model.NewsItem, int, error) {
	if len(results) == 0 {
		return results, 0, nil
	}
	type row struct {
		platformID string
		item       model.NewsItem
		primary    string
		matchSigs  []string
	}
	var rows []row
	for _, pid := range sortedPlatformKeys(results) {
		for _, item := range results[pid] {
			pri := EmailItemSignature(pid, item)
			ms := matchSigs(pid, item)
			rows = append(rows, row{platformID: pid, item: item, primary: pri, matchSigs: ms})
		}
	}
	if len(rows) == 0 {
		return map[string][]model.NewsItem{}, 0, nil
	}
	db := core.GetDB()
	if db == nil {
		return results, 0, nil
	}

	sigSet := make(map[string]struct{})
	for _, r := range rows {
		for _, s := range r.matchSigs {
			sigSet[s] = struct{}{}
		}
	}
	allSigs := keysOfSet(sigSet)
	existSet, err := loadExistingSigs(db, allSigs)
	if err != nil {
		return nil, 0, err
	}

	kept := make(map[string][]model.NewsItem)
	skipped := 0
	batchSeen := make(map[string]struct{})
	for _, r := range rows {
		if _, dup := batchSeen[r.primary]; dup {
			skipped++
			continue
		}
		history := false
		for _, s := range r.matchSigs {
			if existSet[s] {
				history = true
				break
			}
		}
		if history {
			skipped++
			continue
		}
		batchSeen[r.primary] = struct{}{}
		kept[r.platformID] = append(kept[r.platformID], r.item)
	}
	return kept, skipped, nil
}

func sortedPlatformKeys(m map[string][]model.NewsItem) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func keysOfSet(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func loadExistingSigs(db *gorm.DB, sigs []string) (map[string]bool, error) {
	exist := make(map[string]bool)
	if len(sigs) == 0 {
		return exist, nil
	}
	for i := 0; i < len(sigs); i += emailDedupINChunk {
		end := i + emailDedupINChunk
		if end > len(sigs) {
			end = len(sigs)
		}
		chunk := sigs[i:end]
		var existing []model.EmailSentFingerprint
		if err := db.Where("sig IN ?", chunk).Find(&existing).Error; err != nil {
			return nil, err
		}
		for _, e := range existing {
			exist[e.Sig] = true
		}
	}
	return exist, nil
}

// RecordEmailSent 邮件发送成功后记录指纹（可重复调用，冲突忽略）
func RecordEmailSent(results map[string][]model.NewsItem) error {
	db := core.GetDB()
	if db == nil {
		return nil
	}
	now := time.Now()
	var rows []model.EmailSentFingerprint
	for _, pid := range sortedPlatformKeys(results) {
		for _, item := range results[pid] {
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

