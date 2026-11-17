package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"linode-panel/internal/linode"
	"linode-panel/internal/store"
)

// TestSelfHealStoreModes 验证统一配置在两种模式间切换时互斥且参数正确保存。
func TestSelfHealStoreModes(t *testing.T) {
	st, err := openTestStore(t)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser("alice", "alice123", 1); err != nil {
		t.Fatal(err)
	}
	tok, err := st.AddToken(aliceID, "acc", "pat")
	if err != nil {
		t.Fatal(err)
	}

	cfg := store.SelfHeal{
		TokenID: tok.ID, UserID: aliceID, Mode: store.SelfHealRebuild, Enabled: true,
		Port: 443, Threshold: 5, IntervalSec: 600, Script: "echo hi",
	}
	if err := st.SetSelfHeal(cfg, "secret"); err != nil {
		t.Fatalf("SetSelfHeal(rebuild): %v", err)
	}
	got := st.SelfHealOf(tok.ID)
	if got.Mode != store.SelfHealRebuild || !got.Enabled || got.Port != 443 ||
		got.Threshold != 5 || got.IntervalSec != 600 || got.Script != "echo hi" || !got.HasRootPass {
		t.Fatalf("rebuild view = %+v", got)
	}
	if all := st.SelfHeals(); len(all) != 1 || all[0].Mode != store.SelfHealRebuild {
		t.Fatalf("SelfHeals = %+v", all)
	}

	// 切到 dual：自动换 IP 开关应被清掉，A/B 指针生效。
	cfg.Mode, cfg.Enabled, cfg.ActiveInstanceID, cfg.StandbyInstanceID = store.SelfHealDual, true, 11, 12
	if err := st.SetSelfHeal(cfg, ""); err != nil {
		t.Fatalf("SetSelfHeal(dual): %v", err)
	}
	got = st.SelfHealOf(tok.ID)
	ac, _ := st.AccountConfig(tok.ID)
	if got.Mode != store.SelfHealDual || got.ActiveInstanceID != 11 || got.StandbyInstanceID != 12 {
		t.Fatalf("dual view = %+v", got)
	}
	if ac.Enabled {
		t.Fatal("切到 dual 后 auto_enabled 应为 false")
	}

	if err := st.DeleteSelfHeal(tok.ID); err != nil {
		t.Fatal(err)
	}
	if got := st.SelfHealOf(tok.ID); got.Mode != store.SelfHealOff || got.Enabled {
		t.Fatalf("after delete = %+v", got)
	}
	if all := st.SelfHeals(); len(all) != 0 {
		t.Fatalf("after delete SelfHeals = %+v", all)
	}
}

// TestSelfHealAPI 验证统一配置接口可保存/读取/关闭，并只列出当前用户的配置。
func TestSelfHealAPI(t *testing.T) {
	srv := testServer(t, &fakeAPI{})
	addAliceToken(t, srv, "acc", "pat")
	cookie := loginAlice(t, srv)
	tok := srv.Store.TokensOf(aliceID)[0]
	path := "/api/account/self-heal/" + strconv.Itoa(tok.ID)

	body := `{"mode":"rebuild","enabled":true,"port":443,"threshold":4,"interval_seconds":600,"script":"echo hi","root_password":"secret"}`
	rr := authedPut(t, srv, cookie, path, body)
	wantStatus(t, rr, http.StatusOK)
	var cfg store.SelfHeal
	decodeJSON(t, rr, &cfg)
	if cfg.Mode != store.SelfHealRebuild || cfg.Port != 443 || cfg.Threshold != 4 || !cfg.HasRootPass {
		t.Fatalf("put response = %+v", cfg)
	}

	rr = authedGet(t, srv, cookie, path)
	wantStatus(t, rr, http.StatusOK)
	decodeJSON(t, rr, &cfg)
	if cfg.Mode != store.SelfHealRebuild || cfg.Script != "echo hi" {
		t.Fatalf("get response = %+v", cfg)
	}

	rr = authedGet(t, srv, cookie, "/api/account/self-heal")
	wantStatus(t, rr, http.StatusOK)
	var list struct {
		SelfHeal []store.SelfHeal `json:"self_heal"`
	}
	decodeJSON(t, rr, &list)
	if len(list.SelfHeal) != 1 || list.SelfHeal[0].TokenID != tok.ID {
		t.Fatalf("list = %+v", list)
	}

	rr = authedDelete(t, srv, cookie, path)
	wantStatus(t, rr, http.StatusOK)
	cfg = srv.Store.SelfHealOf(tok.ID)
	if cfg.Mode != store.SelfHealOff {
		t.Fatalf("after delete = %+v", cfg)
	}
}

// TestSelfHealDualCheckUsesAccountPort 验证双机模式被墙探测使用账号配置里的
// 检测端口，而不是 SSH 脚本端口（启动参数 --ssh-port）。
func TestSelfHealDualCheckUsesAccountPort(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/linode/instances/10":
			_, _ = w.Write([]byte(`{"id":10,"status":"running","ipv4":["1.2.3.4"]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/linode/instances/11":
			_, _ = w.Write([]byte(`{"id":11,"status":"offline","ipv4":["5.6.7.8"]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/shutdown"):
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[{"reason":"not found"}]}`))
		}
	}))
	defer ts.Close()

	srv, _ := newSelfHealServer(t)
	srv.APIBase = ts.URL
	tok := srv.Store.TokensOf(aliceID)[0]
	if err := srv.Store.SetSelfHeal(store.SelfHeal{
		TokenID: tok.ID, UserID: aliceID, Mode: store.SelfHealDual, Enabled: true,
		Port: 443, Threshold: 3, IntervalSec: 60,
		ActiveInstanceID: 10, StandbyInstanceID: 11,
	}, "Secret123!"); err != nil {
		t.Fatalf("SetSelfHeal: %v", err)
	}
	cfg, ok := srv.Store.SelfHealConfigOf(tok.ID)
	if !ok || cfg.Port != 443 {
		t.Fatalf("stored port = %d, ok=%v, want 443", cfg.Port, ok)
	}
	gotPort := 0
	m := &SelfHealMonitor{srv: srv, log: slog.New(slog.DiscardHandler)}
	m.checkFn = func(_ context.Context, _ string, port int) blockCheckResult {
		gotPort = port
		return blockCheckResult{detail: "ok"}
	}
	if !m.tryCheck(context.Background(), cfg) {
		t.Fatal("tryCheck should run when the account lock is free")
	}
	if gotPort != 443 {
		t.Fatalf("probed port = %d, want configured account port 443", gotPort)
	}
}

// TestSelfHealRebuildCheckUsesAccountPort 验证自动换 IP 模式同样使用账号端口。
func TestSelfHealRebuildCheckUsesAccountPort(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/linode/instances/10" {
			_, _ = w.Write([]byte(`{"id":10,"status":"running","ipv4":["1.2.3.4"]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"reason":"not found"}]}`))
	}))
	defer ts.Close()

	srv, _ := newSelfHealServer(t)
	srv.APIBase = ts.URL
	tok := srv.Store.TokensOf(aliceID)[0]
	if err := srv.Store.CacheInstances(tok.ID, []linode.Instance{{ID: 10, Label: "web"}}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Store.SetSelfHeal(store.SelfHeal{
		TokenID: tok.ID, UserID: aliceID, Mode: store.SelfHealRebuild, Enabled: true,
		Port: 8443, Threshold: 3, IntervalSec: 60,
	}, ""); err != nil {
		t.Fatalf("SetSelfHeal: %v", err)
	}
	cfg := srv.Store.SelfHealOf(tok.ID)
	if cfg.Port != 8443 || len(cfg.Instances) != 1 {
		t.Fatalf("account config = %+v, want port 8443 with one instance", cfg)
	}
	gotPort := 0
	m := &SelfHealMonitor{srv: srv, log: slog.New(slog.DiscardHandler)}
	m.checkFn = func(_ context.Context, _ string, port int) blockCheckResult {
		gotPort = port
		return blockCheckResult{detail: "ok"}
	}
	m.checkInstance(context.Background(), cfg, cfg.Instances[0])
	if gotPort != 8443 {
		t.Fatalf("probed port = %d, want configured account port 8443", gotPort)
	}
}
