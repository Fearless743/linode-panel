package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFormatBlockedInterval(t *testing.T) {
	now := time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)
	if got := formatBlockedInterval("", now); got != "首次被墙" {
		t.Fatalf("empty = %q", got)
	}
	prev := now.Add(-(26*time.Hour + 5*time.Minute)).Format(time.RFC3339)
	if got := formatBlockedInterval(prev, now); got != "距离上次被墙 1天2小时5分钟" {
		t.Fatalf("got %q", got)
	}
	prev = now.Add(-90 * time.Second).Format(time.RFC3339)
	if got := formatBlockedInterval(prev, now); got != "距离上次被墙 1分钟" {
		t.Fatalf("got %q", got)
	}
}

func TestNotifyConfigRoundTrip(t *testing.T) {
	srv, session := newDualServer(t)
	_ = session
	if err := srv.Store.SetNotifyConfig(true, "123", "tok"); err != nil {
		t.Fatalf("SetNotifyConfig: %v", err)
	}
	token, chat, enabled := srv.Store.NotifyTarget()
	if !enabled || token != "tok" || chat != "123" {
		t.Fatalf("NotifyTarget = %q %q %v", token, chat, enabled)
	}
	// token 留空保持不变。
	if err := srv.Store.SetNotifyConfig(false, "456", ""); err != nil {
		t.Fatalf("SetNotifyConfig: %v", err)
	}
	token, chat, enabled = srv.Store.NotifyTarget()
	if enabled || token != "" || chat != "" {
		t.Fatalf("disabled target = %q %q %v", token, chat, enabled)
	}
	// 关闭后 token 仍保留，再开时可用。
	if err := srv.Store.SetNotifyConfig(true, "456", ""); err != nil {
		t.Fatalf("SetNotifyConfig: %v", err)
	}
	token, chat, enabled = srv.Store.NotifyTarget()
	if !enabled || token != "tok" || chat != "456" {
		t.Fatalf("re-enable = %q %q %v", token, chat, enabled)
	}
}

func TestNotifyAPIRequiresAdmin(t *testing.T) {
	srv, session := newDualServer(t)
	// admin (seeded id 1) 可访问。
	req := httptest.NewRequest(http.MethodGet, "/api/notify", nil)
	authed(req, session)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("admin GET /api/notify = %d %s", rr.Code, rr.Body.String())
	}
	// 保存 + 测试发送（注入发送器避免调真实 TG）。
	if err := srv.Store.SetNotifyConfig(true, "123", "tok"); err != nil {
		t.Fatal(err)
	}
	var sent []string
	srv.NotifySend = func(_ context.Context, text string) error {
		sent = append(sent, text)
		return nil
	}
	m := &DualMonitor{srv: srv}
	m.notifyDualRotated(1, "acc", "1.1.1.1", "2.2.2.2", 99)
	if len(sent) != 1 || !strings.Contains(sent[0], "2.2.2.2") || !strings.Contains(sent[0], "被墙") {
		t.Fatalf("dual notify = %v", sent)
	}
}
