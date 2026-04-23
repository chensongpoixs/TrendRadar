package model

import (
	"time"
)

// HotlistSnapshot 热榜按次抓取的时间桶快照（追加写入，不覆盖；用于按天/按小时回溯）
type HotlistSnapshot struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	DateLocal   string `gorm:"type:varchar(10);index:idx_hs_day_hour,priority:1;index" json:"date_local"`
	HourLocal   int    `gorm:"index:idx_hs_day_hour,priority:2" json:"hour_local"`
	SourceID    string `gorm:"type:varchar(50);index" json:"source_id"`
	SourceName  string `gorm:"type:varchar(100)" json:"source_name"`
	Title       string `gorm:"type:text" json:"title"`
	URL         string `gorm:"type:text" json:"url"`
	MobileURL   string `gorm:"type:text" json:"mobile_url"`
	Rank        int    `gorm:"default:0" json:"rank"`
	CapturedAt  time.Time `gorm:"index" json:"captured_at"`
	CreatedAt   time.Time `json:"created_at"`
}

func (HotlistSnapshot) TableName() string {
	return "hotlist_snapshots"
}
