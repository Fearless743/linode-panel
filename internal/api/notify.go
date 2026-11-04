package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// ---------- TG 通知 ----------
//
// 全局单例配置（管理员设置 Bot Token + Chat ID），被墙触发轮换/重建成功后
// 通知新 IP，并显示距上次被墙的间隔。

// formatBlockedInterval 将上次被墙时间格式化为距今的中文时长；无记录则返回
// "首次被墙"。
func formatBlockedInterval(lastBlockedAt string, now time.Time) string {
	if lastBlockedAt == "" {
		return "首次被墙"
	}
	last, err := time.Parse(time.RFC3339, lastBlockedAt)
	if err != nil {
		return "首次被墙"
	}
	d := now.Sub(last)
	if d < 0 {
		d = 0
	}
	return "距离上次被墙 " + formatDurationCN(d)
}

// formatDurationCN 把时长格式化为 X天X小时X分/X小时X分/X分/X秒。
func formatDurationCN(d time.Duration) string {
	secs := int64(d.Seconds())
	if secs < 60 {
		return fmt.Sprintf("%d秒", secs)
	}
	mins := secs / 60
	if mins < 60 {
		return fmt.Sprintf("%d分钟", mins)
	}
	hours := mins / 60
	if hours < 24 {
		return fmt.Sprintf("%d小时%d分钟", hours, mins%60)
	}
	days := hours / 24
	return fmt.Sprintf("%d天%d小时%d分钟", days, hours%24, mins%60)
}

// sendTelegram 发送纯文本 TG 消息。发送器可注入（测试用）；默认调 Bot API。
func (s *Server) sendTelegram(ctx context.Context, text string) error {
	token, chatID, enabled := s.Store.NotifyTarget()
	if !enabled {
		return nil
	}
	if s.NotifySend != nil {
		return s.NotifySend(ctx, text)
	}
	body, _ := json.Marshal(map[string]string{"chat_id": chatID, "text": text})
	url := "https://api.telegram.org/bot" + token + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := s.notifyClient()
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram api HTTP %d", resp.StatusCode)
	}
	return nil
}

func (s *Server) notifyClient() *http.Client {
	if s.NotifyHTTP != nil {
		return s.NotifyHTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// notifyDualRotated 在双机轮换成功后通知：旧 IP → 新 IP + 间隔。
func (m *DualMonitor) notifyDualRotated(cfgTokenID int, label, oldIP, newIP string, newActiveID int) {
	prev := m.srv.Store.DualLastBlockedAt(cfgTokenID)
	now := time.Now().UTC()
	interval := formatBlockedInterval(prev, now)
	_ = m.srv.Store.SetDualLastBlockedAt(cfgTokenID, now.Format(time.RFC3339))
	text := fmt.Sprintf("🚨 双机轮询被墙轮换\n账号：%s\n旧实例 IP：%s\n✅ 新使用中：#%d %s\n%s",
		label, oldIP, newActiveID, newIP, interval)
	if err := m.srv.sendTelegram(context.Background(), text); err != nil {
		slog.Warn("双机轮询 TG 通知失败", "error", err)
	}
}

// notifyDualFailed 在双机轮换失败时通知（无新 IP）。
func (m *DualMonitor) notifyDualFailed(label, reason string) {
	text := fmt.Sprintf("⚠️ 双机轮询轮换失败\n账号：%s\n原因：%s", label, reason)
	if err := m.srv.sendTelegram(context.Background(), text); err != nil {
		slog.Warn("双机轮询 TG 通知失败", "error", err)
	}
}

// notifyAutoRebuilt 在自动换 IP 重建成功后通知：旧 IP → 新 IP + 间隔。
func (m *AutoRebuildMonitor) notifyAutoRebuilt(label string, instanceID int, oldIP, newIP, prevBlockedAt string) {
	now := time.Now().UTC()
	interval := formatBlockedInterval(prevBlockedAt, now)
	text := fmt.Sprintf("🚨 自动换IP被墙重建\n实例：%s #%d\n旧 IP：%s\n✅ 新 IP：%s\n%s",
		label, instanceID, oldIP, newIP, interval)
	if err := m.srv.sendTelegram(context.Background(), text); err != nil {
		slog.Warn("自动换IP TG 通知失败", "error", err)
	}
}
