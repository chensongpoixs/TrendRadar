package model

import "time"

// EmailSentFingerprint 已发送过邮件的条目指纹（去重用）
type EmailSentFingerprint struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Sig         string    `gorm:"size:64;uniqueIndex;not null" json:"sig"`
	FirstSentAt time.Time `gorm:"index" json:"first_sent_at"`
}

func (EmailSentFingerprint) TableName() string {
	return "email_sent_fingerprints"
}
