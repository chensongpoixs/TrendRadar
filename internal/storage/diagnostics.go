package storage

import (
	"errors"
	"strings"
	"time"

	"github.com/trendradar/backend-go/internal/core"
	"github.com/trendradar/backend-go/pkg/model"
	"gorm.io/gorm"
)

const (
	SourceTypePlatform = "platform"
	SourceTypeRSS      = "rss"

	SourceStatusSuccess = "success"
	SourceStatusEmpty   = "empty"
	SourceStatusFailed  = "failed"
)

// SourceHealthInput is the normalized result of one attempted source crawl.
type SourceHealthInput struct {
	SourceType   string
	SourceID     string
	SourceName   string
	Enabled      bool
	Status       string
	ItemCount    int
	LatencyMS    int64
	ErrorMessage string
	StartedAt    time.Time
	FinishedAt   time.Time
}

// SourceHealthSummary aggregates the latest health rows for API status views.
type SourceHealthSummary struct {
	Total              int `json:"total"`
	Success            int `json:"success"`
	Empty              int `json:"empty"`
	Failed             int `json:"failed"`
	Unknown            int `json:"unknown"`
	ConsecutiveFailure int `json:"consecutive_failure"`
}

type DiagnosticsStorage struct{}

func NewDiagnosticsStorage() *DiagnosticsStorage {
	return &DiagnosticsStorage{}
}

func (s *DiagnosticsStorage) RecordSourceRuns(inputs []SourceHealthInput) error {
	for _, input := range inputs {
		if err := s.RecordSourceRun(input); err != nil {
			return err
		}
	}
	return nil
}

func (s *DiagnosticsStorage) RecordSourceRun(input SourceHealthInput) error {
	input.SourceType = strings.TrimSpace(input.SourceType)
	input.SourceID = strings.TrimSpace(input.SourceID)
	input.SourceName = strings.TrimSpace(input.SourceName)
	input.Status = normalizeSourceStatus(input.Status, input.ItemCount, input.ErrorMessage)
	input.ErrorMessage = strings.TrimSpace(input.ErrorMessage)
	if input.SourceType == "" || input.SourceID == "" {
		return nil
	}
	if input.FinishedAt.IsZero() {
		input.FinishedAt = time.Now()
	}
	if input.StartedAt.IsZero() {
		input.StartedAt = input.FinishedAt
	}
	if input.LatencyMS <= 0 {
		input.LatencyMS = input.FinishedAt.Sub(input.StartedAt).Milliseconds()
	}
	if input.LatencyMS < 0 {
		input.LatencyMS = 0
	}

	db := core.GetDB()
	var row model.SourceHealth
	err := db.Where("source_type = ? AND source_id = ?", input.SourceType, input.SourceID).First(&row).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		row = model.SourceHealth{
			SourceType: input.SourceType,
			SourceID:   input.SourceID,
		}
	}

	row.SourceName = input.SourceName
	row.Enabled = input.Enabled
	row.Status = input.Status
	row.ItemCount = input.ItemCount
	row.LatencyMS = input.LatencyMS
	row.ErrorMessage = input.ErrorMessage
	row.LastStartedAt = input.StartedAt
	row.LastFinishedAt = input.FinishedAt

	if input.Status == SourceStatusFailed {
		t := input.FinishedAt
		row.LastFailureAt = &t
		row.ConsecutiveFailures++
	} else {
		t := input.FinishedAt
		row.LastSuccessAt = &t
		row.ConsecutiveFailures = 0
		row.ErrorMessage = ""
	}

	if row.ID == 0 {
		return db.Create(&row).Error
	}
	return db.Save(&row).Error
}

func (s *DiagnosticsStorage) ListSourceHealth(sourceType string) ([]model.SourceHealth, error) {
	var rows []model.SourceHealth
	q := core.GetDB().Model(&model.SourceHealth{})
	if sourceType = strings.TrimSpace(sourceType); sourceType != "" {
		q = q.Where("source_type = ?", sourceType)
	}
	err := q.Order("source_type ASC, source_id ASC").Find(&rows).Error
	return rows, err
}

func BuildSourceHealthSummary(rows []model.SourceHealth) SourceHealthSummary {
	var summary SourceHealthSummary
	for _, row := range rows {
		summary.Total++
		switch row.Status {
		case SourceStatusSuccess:
			summary.Success++
		case SourceStatusEmpty:
			summary.Empty++
		case SourceStatusFailed:
			summary.Failed++
		default:
			summary.Unknown++
		}
		if row.ConsecutiveFailures > 0 {
			summary.ConsecutiveFailure++
		}
	}
	return summary
}

func normalizeSourceStatus(status string, itemCount int, errMessage string) string {
	status = strings.ToLower(strings.TrimSpace(status))
	switch status {
	case SourceStatusSuccess, SourceStatusEmpty, SourceStatusFailed:
		return status
	}
	if strings.TrimSpace(errMessage) != "" {
		return SourceStatusFailed
	}
	if itemCount == 0 {
		return SourceStatusEmpty
	}
	return SourceStatusSuccess
}
