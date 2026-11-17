package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"linode-panel/internal/linode"
	"linode-panel/internal/sshrun"
	"linode-panel/internal/store"
)

// newDualServer seeds admin + one PAT and returns server+session.
func newDualServer(t *testing.T) (*Server, string) {
	t.Helper()
	st, _, err := store.Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if _, err := st.AddToken(1, "acc", "pat-acc"); err != nil {
		t.Fatalf("AddToken: %v", err)
	}
	session := st.NewSession(1, time.Hour)
	s := New(&fakeAPI{}, st)
	t.Cleanup(func() { _ = st.Save() })
	return s, session
}

func TestDualStoreRoundTrip(t *testing.T) {
	st, _, err := store.Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	cfg := store.DualConfig{
		TokenID: 1, UserID: 1, Enabled: true, Port: 22,
		Threshold: 3, IntervalSec: 300, ActiveInstanceID: 101,
		Script: "#!/bin/bash\necho hi",
	}
	if err := st.SetDualConfig(cfg, "Secret123!"); err != nil {
		t.Fatalf("SetDualConfig: %v", err)
	}
	got, ok := st.DualConfigOf(1)
	if !ok || !got.Enabled || got.ActiveInstanceID != 101 {
		t.Fatalf("DualConfigOf = %+v,%v", got, ok)
	}
	if got.Script != "#!/bin/bash\necho hi" || !got.HasRootPass {
		t.Fatalf("script/pass not saved: %+v", got)
	}
	pw, ok := st.DualRootPassword(1)
	if !ok || pw != "Secret123!" {
		t.Fatalf("DualRootPassword = %q,%v", pw, ok)
	}
	if err := st.UpdateDualInstances(1, 102, 101); err != nil {
		t.Fatalf("UpdateDualInstances: %v", err)
	}
	got, _ = st.DualConfigOf(1)
	if got.ActiveInstanceID != 102 || got.StandbyInstanceID != 101 {
		t.Fatalf("swap not saved: %+v", got)
	}
	if err := st.UpdateDualState(1, 2, "rotating", "blocked", "detail", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("UpdateDualState: %v", err)
	}
	got, _ = st.DualConfigOf(1)
	if got.FailCount != 2 || got.State != "rotating" {
		t.Fatalf("state not saved: %+v", got)
	}
}

func TestDualPutRequiresTemplateAndPassword(t *testing.T) {
	srv, session := newDualServer(t)
	// 缺模板。
	body := `{"enabled":true,"port":22,"threshold":3,"interval_seconds":300}`
	req := httptest.NewRequest(http.MethodPut, "/api/account/dual/1", strings.NewReader(body))
	authed(req, session)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400 without template, got %d %s", rr.Code, rr.Body.String())
	}
	// 有模板但缓存/API 都没有该实例。
	body = `{"enabled":true,"template_instance_id":999,"port":22,"threshold":3,"interval_seconds":300,"root_password":"Secret123!"}`
	req = httptest.NewRequest(http.MethodPut, "/api/account/dual/1", strings.NewReader(body))
	authed(req, session)
	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for missing template, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestIsDualManagedSkipsAutoRebuild(t *testing.T) {
	srv, _ := newDualServer(t)
	if err := srv.Store.SetDualConfig(store.DualConfig{
		TokenID: 1, UserID: 1, Enabled: true, Port: 22,
		Threshold: 3, IntervalSec: 300, ActiveInstanceID: 42, StandbyInstanceID: 43,
	}, "Secret123!"); err != nil {
		t.Fatalf("SetDualConfig: %v", err)
	}
	if !srv.isDualManaged(42) || !srv.isDualManaged(43) {
		t.Fatal("dual instances should be managed")
	}
	if srv.isDualManaged(44) {
		t.Fatal("other instance should not be managed")
	}
	if got := srv.dualRoleFor(42); got != "active" {
		t.Fatalf("dualRoleFor(42) = %q, want active", got)
	}
	if got := srv.dualRoleFor(43); got != "standby" {
		t.Fatalf("dualRoleFor(43) = %q, want standby", got)
	}
}

// TestDualRebuildCooldown verifies a rebuild cycle that exhausted the blocked
// IP pool opens the circuit breaker, while ordinary errors stay retryable.
func TestDualRebuildCooldown(t *testing.T) {
	srv, _ := newDualServer(t)
	if err := srv.Store.SetDualConfig(store.DualConfig{
		TokenID: 1, UserID: 1, Enabled: true, Port: 22,
		Threshold: 3, IntervalSec: 60, ActiveInstanceID: 10, StandbyInstanceID: 11,
	}, "Secret123!"); err != nil {
		t.Fatalf("SetDualConfig: %v", err)
	}
	m := &DualMonitor{srv: srv, log: slog.New(slog.DiscardHandler)}
	cfg, _ := srv.Store.DualConfigOf(1)
	if m.inCooldown(cfg) {
		t.Fatal("fresh config should not be cooling down")
	}

	m.rebuildFailed(cfg, fmt.Errorf("准备工作失败: %w", errStandbyPoolExhausted))
	cfg, _ = srv.Store.DualConfigOf(1)
	if cfg.State != dualStateCooldown || !m.inCooldown(cfg) {
		t.Fatalf("pool exhaustion should cool down, state=%q detail=%q", cfg.State, cfg.LastDetail)
	}

	// 普通错误（如 API 创建失败）不进入冷却，下一轮可重试。
	m.clearCooldown(1)
	if err := srv.Store.UpdateDualState(1, 0, "idle", "idle", "reset", time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("UpdateDualState: %v", err)
	}
	cfg, _ = srv.Store.DualConfigOf(1)
	m.rebuildFailed(cfg, fmt.Errorf("创建备用机失败"))
	cfg, _ = srv.Store.DualConfigOf(1)
	if cfg.State != "error" || m.inCooldown(cfg) {
		t.Fatalf("ordinary error should stay retryable, state=%q detail=%q", cfg.State, cfg.LastDetail)
	}
}

// TestRotateKeepsActiveWhenStandbyPoolExhausted is the regression test for the
// "no active instance left" bug: the old active must not be deleted until the
// standby is verified clean and the boot script succeeded. When every rebuilt
// standby IP is blocked, the rotation must abort and keep the current active.
func TestRotateKeepsActiveWhenStandbyPoolExhausted(t *testing.T) {
	var mu sync.Mutex
	insts := map[int]linode.Instance{
		10: {ID: 10, Label: "main", Status: "running", Region: "us-east", Type: "g6-nanode-1", Image: "linode/debian12", IPv4: []string{"5.6.7.8"}},
		11: {ID: 11, Label: "main-standby", Status: "running", Region: "us-east", Type: "g6-nanode-1", Image: "linode/debian12", IPv4: []string{"1.2.3.4"}},
	}
	// A tiny cycling pool, like a region handing back the same blocked IPs.
	pool := []string{"9.9.9.1", "9.9.9.2", "9.9.9.3"}
	ipIdx, nextID := 0, 20
	deleted := map[int]bool{}
	created := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/linode/instances/"):
			rest := strings.TrimPrefix(r.URL.Path, "/linode/instances/")
			id, _ := strconv.Atoi(strings.Split(rest, "/")[0])
			in, ok := insts[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"errors":[{"reason":"not found"}]}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(in)
		case r.Method == http.MethodPost && r.URL.Path == "/linode/instances":
			var opts linode.CreateOptions
			_ = json.NewDecoder(r.Body).Decode(&opts)
			id := nextID
			nextID++
			in := linode.Instance{ID: id, Label: opts.Label, Status: "running", Region: opts.Region,
				Type: opts.Type, Image: opts.Image, IPv4: []string{pool[ipIdx%len(pool)]}}
			ipIdx++
			created++
			insts[id] = in
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(in)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/linode/instances/"):
			id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/linode/instances/"))
			deleted[id] = true
			delete(insts, id)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/boot"):
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/shutdown"):
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && r.URL.Path == "/networking/firewalls":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":77,"label":"linode-panel-fw-us-east","tags":["linode-panel-allow-all"]}],"page":1,"pages":1}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/devices"):
			_, _ = w.Write([]byte(`{"data":[],"page":1,"pages":1}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/devices"):
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[{"reason":"not found"}]}`))
		}
	}))
	defer ts.Close()

	srv, _ := newDualServer(t)
	srv.APIBase = ts.URL
	if err := srv.Store.SetDualConfig(store.DualConfig{
		TokenID: 1, UserID: 1, Enabled: true, Port: 22, Threshold: 3, IntervalSec: 60,
		ActiveInstanceID: 10, StandbyInstanceID: 11,
	}, "Secret123!"); err != nil {
		t.Fatalf("SetDualConfig: %v", err)
	}
	cfg, _ := srv.Store.DualConfigOf(1)
	m := &DualMonitor{srv: srv, log: slog.New(slog.DiscardHandler)}
	m.checkFn = func(context.Context, string, int) blockCheckResult {
		return blockCheckResult{blocked: true, detail: "blocked"}
	}
	active := insts[10]
	m.rotate(context.Background(), cfg, "pat-acc", active)

	if deleted[10] {
		t.Fatal("active instance was deleted even though no clean standby was found")
	}
	got, _ := srv.Store.DualConfigOf(1)
	if got.ActiveInstanceID != 10 {
		t.Fatalf("active pointer = %d, want 10 (unchanged)", got.ActiveInstanceID)
	}
	if got.State != dualStateCooldown {
		t.Fatalf("state = %q, want %q", got.State, dualStateCooldown)
	}
	// 池里有 3 个地址，重复时应停止：最多 3 次重建后拿回第一个地址即停。
	if created > len(pool)+1 {
		t.Fatalf("created %d instances, want <= %d (stop on repeated IP)", created, len(pool)+1)
	}
}

// TestDualCheckUsesConfiguredDetectionPort verifies the dual monitor probes the
// configured detection port (not the SSH/script port).
func TestDualCheckUsesConfiguredDetectionPort(t *testing.T) {
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

	srv, _ := newDualServer(t)
	srv.APIBase = ts.URL
	if err := srv.Store.SetDualConfig(store.DualConfig{
		TokenID: 1, UserID: 1, Enabled: true, Port: 443, Threshold: 3, IntervalSec: 60,
		ActiveInstanceID: 10, StandbyInstanceID: 11,
	}, "Secret123!"); err != nil {
		t.Fatalf("SetDualConfig: %v", err)
	}
	cfg, _ := srv.Store.DualConfigOf(1)
	if cfg.Port != 443 {
		t.Fatalf("stored dual port = %d, want 443", cfg.Port)
	}
	gotPort := 0
	m := &DualMonitor{srv: srv, log: slog.New(slog.DiscardHandler)}
	m.checkFn = func(_ context.Context, _ string, port int) blockCheckResult {
		gotPort = port
		return blockCheckResult{detail: "ok"}
	}
	if !m.tryCheck(context.Background(), cfg) {
		t.Fatal("tryCheck should run when the account lock is free")
	}
	if gotPort != 443 {
		t.Fatalf("probed port = %d, want configured 443", gotPort)
	}
}

// TestDualCheckSkippedWhileBusy verifies a concurrent tick cannot start while
// another monitor operation holds the account lock.
func TestDualCheckSkippedWhileBusy(t *testing.T) {
	srv, _ := newDualServer(t)
	if err := srv.Store.SetDualConfig(store.DualConfig{
		TokenID: 1, UserID: 1, Enabled: true, Port: 22,
		Threshold: 3, IntervalSec: 60, ActiveInstanceID: 10, StandbyInstanceID: 11,
	}, "Secret123!"); err != nil {
		t.Fatalf("SetDualConfig: %v", err)
	}
	var hits int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()
	srv.APIBase = ts.URL

	m := &DualMonitor{srv: srv, log: slog.New(slog.DiscardHandler)}
	cfg, _ := srv.Store.DualConfigOf(1)
	mu := m.tokenLock(1)
	mu.Lock()
	if m.tryCheck(context.Background(), cfg) {
		t.Fatal("tryCheck should report false while the account lock is held")
	}
	mu.Unlock()
	if hits != 0 {
		t.Fatalf("check made %d API calls while the account lock was held", hits)
	}
}

func TestIsSSHAuthError(t *testing.T) {
	authErr := fmt.Errorf("sshrun: handshake 1.2.3.4:22: ssh: handshake failed: ssh: unable to authenticate, attempted methods [none password], no supported methods remain")
	if !isSSHAuthError(authErr) {
		t.Fatal("password rejection should be classified as an auth error")
	}
	if isSSHAuthError(fmt.Errorf("sshrun: dial 1.2.3.4:22: connection refused")) {
		t.Fatal("dial failure is not an auth error")
	}
	if isSSHAuthError(nil) {
		t.Fatal("nil is not an auth error")
	}
}

// TestRotateScriptsBeforeProbingServicePort covers the custom detection-port
// case: a freshly booted standby only has SSH open until the boot script runs,
// so readiness must be checked on SSH, and the service port is only probed
// after scripting. A service-port failure must keep the current active and try
// another standby, never delete A.
func TestRotateScriptsBeforeProbingServicePort(t *testing.T) {
	var mu sync.Mutex
	insts := map[int]linode.Instance{
		10: {ID: 10, Label: "main", Status: "running", Region: "in-maa", Type: "g6-nanode-1", Image: "linode/debian12", IPv4: []string{"5.6.7.8"}},
		11: {ID: 11, Label: "main-standby", Status: "running", Region: "in-maa", Type: "g6-nanode-1", Image: "linode/debian12", IPv4: []string{"1.2.3.4"}},
	}
	nextID := 20
	ipIdx := 0
	created := 0
	deleted := map[int]bool{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/linode/instances/") && strings.HasSuffix(r.URL.Path, "/ips"):
			rest := strings.TrimPrefix(r.URL.Path, "/linode/instances/")
			id, _ := strconv.Atoi(strings.Split(rest, "/")[0])
			in, ok := insts[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"errors":[{"reason":"not found"}]}`))
				return
			}
			var out linode.InstanceIPs
			for _, a := range in.IPv4 {
				out.IPv4.Public = append(out.IPv4.Public, linode.IPAddress{Address: a, Public: true})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/linode/instances/"):
			rest := strings.TrimPrefix(r.URL.Path, "/linode/instances/")
			id, _ := strconv.Atoi(strings.Split(rest, "/")[0])
			in, ok := insts[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"errors":[{"reason":"not found"}]}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(in)
		case r.Method == http.MethodPost && r.URL.Path == "/linode/instances":
			var opts linode.CreateOptions
			_ = json.NewDecoder(r.Body).Decode(&opts)
			id := nextID
			nextID++
			ipIdx++
			in := linode.Instance{ID: id, Label: opts.Label, Status: "running", Region: opts.Region,
				Type: opts.Type, Image: opts.Image, IPv4: []string{fmt.Sprintf("9.9.9.%d", ipIdx)}}
			created++
			insts[id] = in
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(in)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/linode/instances/"):
			id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/linode/instances/"))
			deleted[id] = true
			delete(insts, id)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && (strings.HasSuffix(r.URL.Path, "/boot") || strings.HasSuffix(r.URL.Path, "/shutdown")):
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && r.URL.Path == "/networking/firewalls":
			_, _ = w.Write([]byte(`{"data":[{"id":77,"label":"linode-panel-fw-in-maa","tags":["linode-panel-allow-all"]}],"page":1,"pages":1}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/devices"):
			_, _ = w.Write([]byte(`{"data":[],"page":1,"pages":1}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/devices"):
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[{"reason":"not found"}]}`))
		}
	}))
	defer ts.Close()

	srv, _ := newDualServer(t)
	srv.APIBase = ts.URL
	srv.BootScriptRun = func(context.Context, sshrun.Options, string) (sshrun.Result, error) {
		return sshrun.Result{}, nil
	}
	if err := srv.Store.SetDualConfig(store.DualConfig{
		TokenID: 1, UserID: 1, Enabled: true, Port: 443, Threshold: 3, IntervalSec: 60,
		ActiveInstanceID: 10, StandbyInstanceID: 11,
	}, "Secret123!"); err != nil {
		t.Fatalf("SetDualConfig: %v", err)
	}
	cfg, _ := srv.Store.DualConfigOf(1)
	m := &DualMonitor{srv: srv, log: slog.New(slog.DiscardHandler)}
	m.checkFn = func(_ context.Context, _ string, port int) blockCheckResult {
		if port == srv.sshPort() {
			return blockCheckResult{detail: "ssh ok"}
		}
		return blockCheckResult{blocked: true, detail: "service blocked"}
	}
	m.rotate(context.Background(), cfg, "pat-acc", insts[10])

	if deleted[10] {
		t.Fatal("active must not be deleted when the standby service port is blocked")
	}
	got, _ := srv.Store.DualConfigOf(1)
	if got.ActiveInstanceID != 10 {
		t.Fatalf("active pointer = %d, want 10", got.ActiveInstanceID)
	}
	if got.State != dualStateCooldown {
		t.Fatalf("state = %q, want cooldown", got.State)
	}
	if created != MaxServiceProbeRetries {
		t.Fatalf("created %d standbys, want %d service-port attempts", created, MaxServiceProbeRetries)
	}
}

// TestDualStandbyLabel tests standby label derivation.
func TestDualStandbyLabel(t *testing.T) {
	cases := map[string]string{
		"web":         "web-standby",
		"web-standby": "web-standby",
		"测试机":         "---standby",
		"":            "dual-standby",
	}
	for in, wantSuffix := range cases {
		got := dualStandbyLabel(in)
		if len(got) > 64 {
			t.Fatalf("label too long: %q", got)
		}
		for _, r := range got {
			ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.'
			if !ok {
				t.Fatalf("label %q from %q has illegal char %q", got, in, r)
			}
		}
		_ = wantSuffix
	}
	if got := dualStandbyLabel("web"); got != "web-standby" {
		t.Fatalf("got %q", got)
	}
}
