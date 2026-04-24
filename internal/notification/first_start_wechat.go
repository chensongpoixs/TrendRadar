package notification

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/trendradar/backend-go/pkg/config"
)

// 首次将「与邮件同内容的」摘要推送到 Server 酱后写入该标记，避免重复与合并队列双份
const firstStartWeChatSentFile = "first_start_wechat_sent"

func firstStartWeChatPath() string {
	dir := "./data"
	if cfg := config.Get(); cfg != nil && strings.TrimSpace(cfg.Storage.Local.DataDir) != "" {
		dir = cfg.Storage.Local.DataDir
	}
	return filepath.Join(dir, firstStartWeChatSentFile)
}

// IsFirstStartWeChatLikeEmailPending 是否需要在首次成功发信后，再补一条与邮件相同的微信（仅 batch 模式有意义）
func IsFirstStartWeChatLikeEmailPending() bool {
	cfg := config.Get()
	if cfg == nil || !cfg.Notification.Enabled {
		return false
	}
	sc := cfg.Notification.Channels.ServerChan
	if !sc.NotifyOnStartup || !sc.BatchEnabled || strings.TrimSpace(sc.SendKey) == "" {
		return false
	}
	_, err := os.Stat(firstStartWeChatPath())
	return os.IsNotExist(err)
}

// MarkFirstStartWeChatLikeEmailDone 标记已完成首次「与邮件同文」的微信触达
func MarkFirstStartWeChatLikeEmailDone() error {
	p := firstStartWeChatPath()
	_ = os.MkdirAll(filepath.Dir(p), 0750)
	if _, err := os.Stat(p); err == nil {
		return nil
	}
	return os.WriteFile(p, []byte("1\n"), 0600)
}
