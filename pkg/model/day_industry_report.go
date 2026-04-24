package model

import (
	"time"
)

// DayIndustryReport 某自然日热榜聚类后的行业 AI 研报（可缓存，避免重复打模型）
type DayIndustryReport struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	DateLocal string `gorm:"type:varchar(10);uniqueIndex" json:"date_local"`
	Content   string `gorm:"type:text" json:"content"`
	Model     string `gorm:"type:varchar(120)" json:"model"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (DayIndustryReport) TableName() string {
	return "day_industry_reports"
}
