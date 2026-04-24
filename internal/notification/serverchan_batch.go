package notification

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/trendradar/backend-go/pkg/config"
	applog "github.com/trendradar/backend-go/pkg/logger"
	"go.uber.org/zap"
)

type batchSegment struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

type serverChanBatchFile struct {
	Segments    []batchSegment `json:"segments"`
	PushYMD     string         `json:"push_ymd"`
	PushesToday int            `json:"pushes_today"`
}

var serverChanBatchMu sync.Mutex

func serverChanStatePath() string {
	cfg := config.Get()
	dir := "./data"
	if cfg != nil && strings.TrimSpace(cfg.Storage.Local.DataDir) != "" {
		dir = cfg.Storage.Local.DataDir
	}
	return filepath.Join(dir, "serverchan_batch_state.json")
}

func loadServerChanState(path string) (*serverChanBatchFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &serverChanBatchFile{}, nil
		}
		return nil, err
	}
	var st serverChanBatchFile
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	if st.Segments == nil {
		st.Segments = []batchSegment{}
	}
	return &st, nil
}

func saveServerChanState(path string, st *serverChanBatchFile) error {
	if st == nil {
		return nil
	}
	_ = os.MkdirAll(filepath.Dir(path), 0750)
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0600)
}

// AppendServerChanBatchSegment 在「合并推送」模式下，把本次小时级纯文本摘要入队，供整点槽位与后续段合并
func AppendServerChanBatchSegment(plainText string, at time.Time) error {
	if strings.TrimSpace(plainText) == "" {
		return nil
	}
	cfg := config.Get()
	if cfg == nil || !cfg.Notification.Channels.ServerChan.BatchEnabled {
		return nil
	}
	serverChanBatchMu.Lock()
	defer serverChanBatchMu.Unlock()
	path := serverChanStatePath()
	st, err := loadServerChanState(path)
	if err != nil {
		return err
	}
	st.Segments = append(st.Segments, batchSegment{At: at, Text: strings.TrimSpace(plainText)})
	return saveServerChanState(path, st)
}

// RunServerChanBatchJob 在配置的「整点小时」执行：合并最近 N 段摘要并推送，且每日最多 max_pushes_per_day 次
func RunServerChanBatchJob(now time.Time) {
	cfg := config.Get()
	if cfg == nil || !cfg.Notification.Enabled {
		return
	}
	sc := cfg.Notification.Channels.ServerChan
	if !sc.BatchEnabled || strings.TrimSpace(sc.SendKey) == "" {
		return
	}
	loc := time.Local
	if tz := strings.TrimSpace(cfg.App.Timezone); tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	n := now.In(loc)
	if !HourInSlot(n, sc.SlotHours) {
		return
	}
	mergeN := sc.MergeSegments
	if mergeN < 1 {
		mergeN = 2
	}
	maxDay := sc.MaxPushesPerDay
	if maxDay < 1 {
		maxDay = 5
	}
	serverChanBatchMu.Lock()
	defer serverChanBatchMu.Unlock()
	path := serverChanStatePath()
	st, err := loadServerChanState(path)
	if err != nil {
		applog.WithComponent("notify").Error("serverchan batch load state", zap.Error(err))
		return
	}
	ymd := n.Format("2006-01-02")
	if st.PushYMD != ymd {
		st.PushesToday = 0
		st.PushYMD = ymd
	}
	if st.PushesToday >= maxDay {
		applog.WithComponent("notify").Info("serverchan batch: daily cap",
			zap.String("date", ymd), zap.Int("pushes", st.PushesToday), zap.Int("max", maxDay))
		return
	}
	if len(st.Segments) == 0 {
		applog.WithComponent("notify").Info("serverchan batch: no segments", zap.String("at", n.Format("15:04")))
		return
	}
	take := mergeN
	if len(st.Segments) < take {
		take = len(st.Segments)
	}
	start := len(st.Segments) - take
	picked := st.Segments[start:]
	var b strings.Builder
	for i, seg := range picked {
		if i > 0 {
			b.WriteString("\n\n---\n\n")
		}
		b.WriteString(fmt.Sprintf("### 时段 %s\n\n", seg.At.In(loc).Format("01-02 15:04")))
		b.WriteString(seg.Text)
	}
	merged := b.String()
	title := fmt.Sprintf("趋势雷达·合并 %d 段摘要 %s", take, n.Format("15:04"))
	d := NewDispatcher()
	if !d.sendToServerChan(title, merged) {
		applog.WithComponent("notify").Warn("serverchan batch send failed, segments kept for retry")
		return
	}
	st.Segments = st.Segments[:start]
	st.PushesToday++
	if e := saveServerChanState(path, st); e != nil {
		applog.WithComponent("notify").Error("serverchan batch save state", zap.Error(e))
	}
	applog.WithComponent("notify").Info("serverchan batch sent",
		zap.String("title", title), zap.Int("merged_segments", take), zap.Int("pushes_today", st.PushesToday))
}

// HourInSlot 判断当前时间的小时数是否在配置槽位
func HourInSlot(n time.Time, slotHours string) bool {
	if strings.TrimSpace(slotHours) == "" {
		slotHours = "8,11,14,17,20"
	}
	parts := strings.Split(slotHours, ",")
	candidates := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		h, err := strconv.Atoi(p)
		if err != nil {
			continue
		}
		if h >= 0 && h <= 23 {
			candidates = append(candidates, h)
		}
	}
	if len(candidates) == 0 {
		return false
	}
	sort.Ints(candidates)
	h := n.Hour()
	for _, c := range candidates {
		if c == h {
			return true
		}
	}
	return false
}

// BuildServerChanCronSpec 由 slot_hours 生成 robfig/cron 表达式（与 scheduler 的 WithSeconds 一致：秒 分 时 日 月 周）
func BuildServerChanCronSpec(slotHours string) string {
	if strings.TrimSpace(slotHours) == "" {
		slotHours = "8,11,14,17,20"
	}
	parts := strings.Split(slotHours, ",")
	hours := make([]int, 0)
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		h, err := strconv.Atoi(p)
		if err != nil {
			continue
		}
		if h >= 0 && h <= 23 {
			hours = append(hours, h)
		}
	}
	if len(hours) == 0 {
		return ""
	}
	sort.Ints(hours)
	var sb strings.Builder
	for i, h := range hours {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(strconv.Itoa(h))
	}
	// 每槽位「时+8 分」执行，避免与整点抓取抢跑；此时本小时摘要已入队，可合并「最近 N 段」
	return fmt.Sprintf("0 8 %s * * *", sb.String())
}
