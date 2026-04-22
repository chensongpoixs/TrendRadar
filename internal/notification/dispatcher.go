package notification

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"strings"
	"time"

	"github.com/trendradar/backend-go/pkg/config"
	applog "github.com/trendradar/backend-go/pkg/logger"
	"go.uber.org/zap"
)

// Dispatcher 通知调度器
type Dispatcher struct {
	config *config.NotificationConfig
}

// NewDispatcher 创建通知调度器
func NewDispatcher() *Dispatcher {
	cfg := config.Get().Notification
	return &Dispatcher{config: &cfg}
}

// Send 发送通知到所有已配置的渠道
func (d *Dispatcher) Send(title, message string) map[string]bool {
	results := make(map[string]bool)

	// 飞书
	if d.config.Channels.Feishu.WebhookURL != "" {
		results["feishu"] = d.sendToFeishu(title, message)
	}

	// 钉钉
	if d.config.Channels.DingTalk.WebhookURL != "" {
		results["dingtalk"] = d.sendToDingTalk(title, message)
	}

	// 企业微信
	if d.config.Channels.WeWork.WebhookURL != "" {
		results["wework"] = d.sendToWeWork(title, message)
	}

	// Telegram
	if d.config.Channels.Telegram.BotToken != "" && d.config.Channels.Telegram.ChatID != "" {
		results["telegram"] = d.sendToTelegram(title, message)
	}

	// 邮件
	if d.config.Channels.Email.From != "" && d.config.Channels.Email.To != "" {
		results["email"] = d.sendToEmail(title, message)
	}

	// ntfy
	if d.config.Channels.Ntfy.Topic != "" {
		results["ntfy"] = d.sendToNtfy(title, message)
	}

	// Bark
	if d.config.Channels.Bark.URL != "" {
		results["bark"] = d.sendToBark(title, message)
	}

	// Slack
	if d.config.Channels.Slack.WebhookURL != "" {
		results["slack"] = d.sendToSlack(title, message)
	}

	return results
}

// sendToFeishu 发送到飞书
func (d *Dispatcher) sendToFeishu(title, message string) bool {
	webhookURL := d.config.Channels.Feishu.WebhookURL

	// 飞书消息格式
	data := map[string]interface{}{
		"msg_type": "post",
		"content": map[string]interface{}{
			"post": map[string]interface{}{
				"content": []map[string]interface{}{
					{
						"tag": "text",
						"text": fmt.Sprintf("**%s**", title),
					},
					{
						"tag": "text",
						"text": message,
					},
				},
			},
		},
	}

	return sendWebhook(webhookURL, data)
}

// sendToDingTalk 发送到钉钉
func (d *Dispatcher) sendToDingTalk(title, message string) bool {
	webhookURL := d.config.Channels.DingTalk.WebhookURL

	// 钉钉消息格式
	data := map[string]interface{}{
		"msgtype": "markdown",
		"markdown": map[string]interface{}{
			"title": title,
			"text":  fmt.Sprintf("# %s\n\n%s", title, message),
		},
	}

	return sendWebhook(webhookURL, data)
}

// sendToWeWork 发送到企业微信
func (d *Dispatcher) sendToWeWork(title, message string) bool {
	webhookURL := d.config.Channels.WeWork.WebhookURL

	// 企业微信消息格式
	msgType := d.config.Channels.WeWork.MsgType
	if msgType == "" {
		msgType = "markdown"
	}

	var data map[string]interface{}
	if msgType == "markdown" {
		data = map[string]interface{}{
			"msgtype": "markdown",
			"markdown": map[string]interface{}{
				"content": fmt.Sprintf("# %s\n\n%s", title, message),
			},
		}
	} else {
		data = map[string]interface{}{
			"msgtype": "text",
			"text": map[string]interface{}{
				"content": fmt.Sprintf("%s\n\n%s", title, message),
			},
		}
	}

	return sendWebhook(webhookURL, data)
}

// sendToTelegram 发送到 Telegram
func (d *Dispatcher) sendToTelegram(title, message string) bool {
	botToken := d.config.Channels.Telegram.BotToken
	chatID := d.config.Channels.Telegram.ChatID

	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken)

	data := map[string]interface{}{
		"chat_id": chatID,
		"text":    fmt.Sprintf("*%s*\n\n%s", title, message),
		"parse_mode": "Markdown",
	}

	return sendWebhook(url, data)
}

// sendToNtfy 发送到 ntfy
func (d *Dispatcher) sendToNtfy(title, message string) bool {
	serverURL := d.config.Channels.Ntfy.ServerURL
	if serverURL == "" {
		serverURL = "https://ntfy.sh"
	}
	topic := d.config.Channels.Ntfy.Topic

	url := fmt.Sprintf("%s/%s", serverURL, topic)

	data := map[string]interface{}{
		"title": title,
		"message": message,
	}

	return sendWebhook(url, data)
}

// sendToBark 发送到 Bark
func (d *Dispatcher) sendToBark(title, message string) bool {
	url := d.config.Channels.Bark.URL

	// Bark 格式：https://api.day.app/{device_key}/{message}
	fullURL := fmt.Sprintf("%s/%s", url, message)

	data := map[string]interface{}{
		"title": title,
	}

	return sendWebhook(fullURL, data)
}

// sendToSlack 发送到 Slack
func (d *Dispatcher) sendToSlack(title, message string) bool {
	webhookURL := d.config.Channels.Slack.WebhookURL

	// Slack 消息格式
	data := map[string]interface{}{
		"blocks": []map[string]interface{}{
			{
				"type": "header",
				"text": map[string]interface{}{
					"type": "plain_text",
					"text": title,
				},
			},
			{
				"type": "section",
				"text": map[string]interface{}{
					"type": "mrkdwn",
					"text": message,
				},
			},
		},
	}

	return sendWebhook(webhookURL, data)
}

// sendWebhook 通用 webhook 发送函数
func sendWebhook(url string, data map[string]interface{}) bool {
	// 序列化数据
	jsonData, err := json.Marshal(data)
	if err != nil {
		applog.WithComponent("notify").Error("webhook marshal failed", zap.Error(err))
		return false
	}

	// 创建 HTTP 请求
	req, err := http.NewRequest("POST", url, bytes.NewReader(jsonData))
	if err != nil {
		applog.WithComponent("notify").Error("webhook new request failed", zap.Error(err))
		return false
	}

	req.Header.Set("Content-Type", "application/json")

	// 执行请求
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		applog.WithComponent("notify").Error("webhook post failed", zap.String("url", url), zap.Error(err))
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		applog.WithComponent("notify").Info("webhook ok", zap.String("url", url), zap.Int("status", resp.StatusCode))
		return true
	}

	body, _ := io.ReadAll(resp.Body)
	applog.WithComponent("notify").Warn("webhook non-2xx", zap.String("url", url), zap.Int("status", resp.StatusCode), zap.String("body", string(body)))
	return false
}

// sendToEmail 发送邮件
func (d *Dispatcher) sendToEmail(title, message string) bool {
	cfg := d.config.Channels.Email

	// 解析收件人
	toEmail := strings.TrimSpace(cfg.To)
	if toEmail == "" {
		applog.WithComponent("notify").Warn("email to empty")
		return false
	}

	// SMTP 服务器地址和端口
	smtpServer := cfg.SMTPServer
	smtpPort := cfg.SMTPPort

	// 默认值
	if smtpServer == "" {
		smtpServer = "smtp.gmail.com"
	}
	if smtpPort == "" {
		smtpPort = "587"
	}

	// 构建发件人与收件人
	from := strings.TrimSpace(cfg.From)
	recipients := make([]string, 0)
	for _, addr := range strings.Split(toEmail, ",") {
		trimmed := strings.TrimSpace(addr)
		if trimmed != "" {
			recipients = append(recipients, trimmed)
		}
	}
	if len(recipients) == 0 {
		applog.WithComponent("notify").Warn("email no valid recipients")
		return false
	}

	// RFC822 格式邮件正文
	isHTML := strings.Contains(strings.ToLower(message), "<html")
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "From: 趋势雷达 <%s>\r\n", from)
	fmt.Fprintf(&buf, "To: %s\r\n", strings.Join(recipients, ","))
	fmt.Fprintf(&buf, "Subject: %s\r\n", title)
	fmt.Fprint(&buf, "MIME-Version: 1.0\r\n")
	if isHTML {
		fmt.Fprint(&buf, "Content-Type: text/html; charset=UTF-8\r\n")
	} else {
		fmt.Fprint(&buf, "Content-Type: text/plain; charset=UTF-8\r\n")
	}
	fmt.Fprint(&buf, "\r\n")
	fmt.Fprint(&buf, message)

	// 发送邮件：优先按端口自动选择协议，兼容 163/QQ 等常见邮箱
	auth := smtp.PlainAuth("", from, cfg.Password, smtpServer)
	var sendErr error
	if smtpPort == "465" {
		sendErr = sendMailImplicitTLS(smtpServer, smtpPort, from, recipients, auth, buf.Bytes())
	} else {
		sendErr = sendMailStartTLS(smtpServer, smtpPort, from, recipients, auth, buf.Bytes())
	}
	if sendErr != nil {
		applog.WithComponent("notify").Error("smtp send failed", zap.Error(sendErr), zap.String("to", toEmail))
		return false
	}

	applog.WithComponent("notify").Info("email sent", zap.String("to", toEmail))
	return true
}

func sendMailStartTLS(host, port, from string, to []string, auth smtp.Auth, msg []byte) error {
	addr := net.JoinHostPort(host, port)
	c, err := smtp.Dial(addr)
	if err != nil {
		return err
	}
	defer c.Close()

	if ok, _ := c.Extension("STARTTLS"); ok {
		tlsConfig := &tls.Config{
			ServerName: host,
			MinVersion: tls.VersionTLS12,
		}
		if err := c.StartTLS(tlsConfig); err != nil {
			return err
		}
	}

	if ok, _ := c.Extension("AUTH"); ok {
		if err := c.Auth(auth); err != nil {
			return err
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, addrTo := range to {
		if err := c.Rcpt(addrTo); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		_ = w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func sendMailImplicitTLS(host, port, from string, to []string, auth smtp.Auth, msg []byte) error {
	addr := net.JoinHostPort(host, port)
	conn, err := tls.Dial("tcp", addr, &tls.Config{
		ServerName: host,
		MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		return err
	}
	defer conn.Close()

	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer c.Close()

	if ok, _ := c.Extension("AUTH"); ok {
		if err := c.Auth(auth); err != nil {
			return err
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, addrTo := range to {
		if err := c.Rcpt(addrTo); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		_ = w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
