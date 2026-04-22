package notification

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/smtp"
	"strings"
	"time"

	"github.com/trendradar/backend-go/pkg/config"
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
		log.Printf("Failed to marshal webhook data: %v", err)
		return false
	}

	// 创建 HTTP 请求
	req, err := http.NewRequest("POST", url, bytes.NewReader(jsonData))
	if err != nil {
		log.Printf("Failed to create webhook request: %v", err)
		return false
	}

	req.Header.Set("Content-Type", "application/json")

	// 执行请求
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Failed to send webhook to %s: %v", url, err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		log.Printf("Webhook sent successfully to %s (status: %d)", url, resp.StatusCode)
		return true
	}

	body, _ := io.ReadAll(resp.Body)
	log.Printf("Webhook failed to %s (status: %d): %s", url, resp.StatusCode, string(body))
	return false
}

// sendToEmail 发送邮件
func (d *Dispatcher) sendToEmail(title, message string) bool {
	cfg := d.config.Channels.Email

	// 解析收件人
	toEmail := strings.TrimSpace(cfg.To)
	if toEmail == "" {
		log.Printf("Email recipient is empty")
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
		log.Printf("No valid email recipients")
		return false
	}

	// RFC822 格式邮件正文
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "From: TrendRadar <%s>\r\n", from)
	fmt.Fprintf(&buf, "To: %s\r\n", strings.Join(recipients, ","))
	fmt.Fprintf(&buf, "Subject: %s\r\n", title)
	fmt.Fprint(&buf, "MIME-Version: 1.0\r\n")
	fmt.Fprint(&buf, "Content-Type: text/plain; charset=UTF-8\r\n")
	fmt.Fprint(&buf, "\r\n")
	fmt.Fprint(&buf, message)

	// 发送邮件
	auth := smtp.PlainAuth("", from, cfg.Password, smtpServer)
	if err := smtp.SendMail(smtpServer+":"+smtpPort, auth, from, recipients, buf.Bytes()); err != nil {
		log.Printf("Failed to send email: %v", err)
		return false
	}

	log.Printf("Email sent successfully to %s", toEmail)
	return true
}
