package api

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"linode-panel/internal/linode"
	"linode-panel/internal/store"
)

// TestParseBlockCheck covers the ipcheck response verdicts.
func TestParseBlockCheck(t *testing.T) {
	const ip = "18.138.53.142"
	ok := []byte(`{"code":1,"message":"检查成功！","data":{"ping":true,"tcp":true,"ip":"18.138.53.142"}}`)
	blocked := []byte(`{"code":1,"message":"检查成功！","data":{"ping":false,"tcp":false,"ip":"0.0.0.0"}}`)
	portClosed := []byte(`{"code":1,"message":"检查成功！","data":{"ping":true,"tcp":false,"ip":"18.138.53.142"}}`)
	apiErr := []byte(`{"code":0,"message":"失败"}`)
	junk := []byte(`<html>403</html>`)

	if res := parseBlockCheck(ip, 443, ok); res.blocked || res.err != nil {
		t.Fatalf("tcp 通应正常: %+v", res)
	}
	if res := parseBlockCheck(ip, 443, blocked); !res.blocked || res.err != nil {
		t.Fatalf("tcp 不通应判定被墙: %+v", res)
	}
	if !strings.Contains(parseBlockCheck(ip, 443, blocked).detail, ip) {
		t.Fatal("被墙详情应包含被检测的 IP（而非接口返回的 0.0.0.0）")
	}
	if res := parseBlockCheck(ip, 443, portClosed); !res.blocked || res.err != nil {
		t.Fatalf("仅测 tcp: 端口关闭(ping=true)也应判定被墙: %+v", res)
	}
	if res := parseBlockCheck(ip, 443, apiErr); res.err == nil {
		t.Fatal("code!=1 应返回错误")
	}
	if res := parseBlockCheck(ip, 443, junk); res.err == nil {
		t.Fatal("非 JSON 应返回错误")
	}
}

// monitorHarness wires a Server (store with admin PAT + fake Linode server)
// and a Manager with a blocking fake so a triggered rebuild stays in flight.
type monitorHarness struct {
	srv     *Server
	mgr     *Manager
	api     *fakeAPI
	release chan struct{}
	once    sync.Once
	started chan struct{}
}

func newMonitorHarness(t *testing.T, ip string) *monitorHarness {
	t.Helper()
	ts := fakeLinodeServer(t, map[string][]linode.Instance{
		"pat1": {{ID: 42, Label: "web", Status: "running", Region: "us-east", Type: "g6-standard-1", IPv4: []string{ip}}},
	})
	st, _, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if _, err := st.AddToken(1, "main", "pat1"); err != nil {
		t.Fatalf("AddToken: %v", err)
	}
	h := &monitorHarness{
		api:     &fakeAPI{},
		release: make(chan struct{}),
		started: make(chan struct{}),
	}
	h.mgr = NewManager(h.api)
	h.mgr.PollInterval = time.Millisecond
	h.mgr.PollTimeout = 50 * time.Millisecond
	// Block inside the first GetInstance so the task remains "running".
	h.api.getInstance = func(_ context.Context, id int) (linode.Instance, error) {
		h.once.Do(func() { close(h.started) })
		<-h.release
		return linode.Instance{ID: id, Region: "r", Type: "t", Image: "i", Status: "running", IPv4: []string{"198.51.100.5"}}, nil
	}
	// A create failure ends the task quickly after release.
	h.api.createInstance = func(_ context.Context, _ linode.CreateOptions) (linode.Instance, error) {
		return linode.Instance{}, context.Canceled
	}
	h.mgr.newAPI = func(string) LinodeAPI { return h.api }

	h.srv = New(h.api, st)
	h.srv.APIBase = ts.URL
	h.srv.Tasks = h.mgr
	return h
}

// TestMonitorTriggersRebuildWhenBlocked verifies the failure threshold starts a
// rebuild and records the "rebuilding" state with the task id.
func TestMonitorTriggersRebuildWhenBlocked(t *testing.T) {
	h := newMonitorHarness(t, "1.2.3.4")
	// Configure instance 42 for auto-rebuild, threshold 2.
	if err := h.srv.Store.SetAutoRebuild(store.AutoRebuild{
		InstanceID: 42, UserID: 1, TokenID: 1, Enabled: true,
		Port: 22, Threshold: 2, IntervalSec: 60,
	}); err != nil {
		t.Fatalf("SetAutoRebuild: %v", err)
	}
	m := &AutoRebuildMonitor{
		srv: h.srv, checkAPI: "x", client: &http.Client{}, log: slog.New(slog.DiscardHandler),
	}
	m.checkFn = func(context.Context, string, int) blockCheckResult {
		return blockCheckResult{blocked: true, detail: "国内探测: 端口不通"}
	}

	// First blocked check: below threshold, only state recorded.
	cfg, _ := h.srv.Store.AutoRebuild(42)
	m.tickOnce(context.Background())
	cfg, _ = h.srv.Store.AutoRebuild(42)
	if cfg.FailCount != 1 || cfg.LastStatus != "blocked" {
		t.Fatalf("after 1st check = %+v", cfg)
	}

	// The interval has not elapsed, so a second tick is a no-op.
	m.tickOnce(context.Background())
	cfg, _ = h.srv.Store.AutoRebuild(42)
	if cfg.FailCount != 1 {
		t.Fatalf("interval not respected, fail count = %d", cfg.FailCount)
	}

	// Force due and tick again: threshold reached -> rebuild triggered.
	_ = h.srv.Store.UpdateAutoRebuildState(42, 1, "blocked", "d", "2000-01-01T00:00:00Z", "", "")
	m.tickOnce(context.Background())
	select {
	case <-h.started:
	case <-time.After(2 * time.Second):
		t.Fatal("rebuild never started")
	}
	cfg, _ = h.srv.Store.AutoRebuild(42)
	if cfg.LastStatus != "rebuilding" || cfg.LastTaskID == "" || cfg.FailCount != 0 {
		t.Fatalf("after trigger = %+v", cfg)
	}
	if cfg.LastBlockedAt == "" {
		t.Fatal("LastBlockedAt should be set")
	}

	// Finish the task (it fails at create → no instance move), so no goroutine
	// lingers.
	close(h.release)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if task, err := h.mgr.Get(cfg.LastTaskID); err == nil && task.Status != StatusRunning {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestMonitorSkipsNonRunning verifies a stopped instance is not mistaken for
// blocked.
func TestMonitorSkipsNonRunning(t *testing.T) {
	ts := fakeLinodeServer(t, map[string][]linode.Instance{
		"pat1": {{ID: 42, Label: "web", Status: "offline", Region: "us-east", Type: "t", IPv4: []string{"1.2.3.4"}}},
	})
	st, _, _ := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if _, err := st.AddToken(1, "main", "pat1"); err != nil {
		t.Fatalf("AddToken: %v", err)
	}
	srv := New(&fakeAPI{}, st)
	srv.APIBase = ts.URL
	_ = srv.Store.SetAutoRebuild(store.AutoRebuild{
		InstanceID: 42, UserID: 1, TokenID: 1, Enabled: true, Port: 22, Threshold: 1, IntervalSec: 60,
	})
	called := false
	m := &AutoRebuildMonitor{srv: srv, client: &http.Client{}, log: slog.New(slog.DiscardHandler)}
	m.checkFn = func(context.Context, string, int) blockCheckResult {
		called = true
		return blockCheckResult{blocked: true}
	}
	m.tickOnce(context.Background())
	if called {
		t.Fatal("probe should be skipped for a non-running instance")
	}
	cfg, _ := srv.Store.AutoRebuild(42)
	if cfg.LastStatus != "idle" || cfg.FailCount != 0 {
		t.Fatalf("state = %+v, want idle/0", cfg)
	}
}

// TestAutoRebuildHTTP covers get/put/delete/list for the config endpoints.
func TestAutoRebuildHTTP(t *testing.T) {
	h := newRebuildHarness(t)
	srv, session := rebuildServer(t, h)

	// GET before configuring returns defaults.
	req := httptest.NewRequest(http.MethodGet, "/api/instances/42/auto-rebuild", nil)
	authed(req, session)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	wantStatus(t, rr, http.StatusOK)
	var got store.AutoRebuild
	decodeJSON(t, rr, &got)
	if got.Enabled || got.Port != 22 || got.Threshold != DefaultAutoRebuildThreshold {
		t.Fatalf("defaults = %+v", got)
	}

	// PUT enables it.
	body := `{"enabled":true,"port":443,"threshold":2,"interval_seconds":120}`
	req2 := httptest.NewRequest(http.MethodPut, "/api/instances/42/auto-rebuild", bytes.NewBufferString(body))
	authed(req2, session)
	rr2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr2, req2)
	wantStatus(t, rr2, http.StatusOK)
	decodeJSON(t, rr2, &got)
	if !got.Enabled || got.Port != 443 || got.Threshold != 2 || got.IntervalSec != 120 {
		t.Fatalf("after put = %+v", got)
	}

	// List marks instance 42.
	req3 := httptest.NewRequest(http.MethodGet, "/api/auto-rebuild", nil)
	authed(req3, session)
	rr3 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr3, req3)
	var list struct {
		Items []store.AutoRebuild `json:"auto_rebuild"`
	}
	decodeJSON(t, rr3, &list)
	if len(list.Items) != 1 || list.Items[0].InstanceID != 42 || !list.Items[0].Enabled {
		t.Fatalf("list = %+v", list)
	}

	// Invalid port is rejected.
	req4 := httptest.NewRequest(http.MethodPut, "/api/instances/42/auto-rebuild", bytes.NewBufferString(`{"enabled":true,"port":99999}`))
	authed(req4, session)
	rr4 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr4, req4)
	wantStatus(t, rr4, http.StatusBadRequest)

	// DELETE removes it.
	req5 := httptest.NewRequest(http.MethodDelete, "/api/instances/42/auto-rebuild", nil)
	authed(req5, session)
	rr5 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr5, req5)
	wantStatus(t, rr5, http.StatusOK)
	if _, ok := srv.Store.AutoRebuild(42); ok {
		t.Fatal("config should be deleted")
	}
}
