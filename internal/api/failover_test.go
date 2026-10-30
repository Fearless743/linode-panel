package api

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"linode-panel/internal/linode"
	"linode-panel/internal/store"
)

// failoverHarness wires a store with user 1, two tokens (pat1/pat2) and an
// account group holding both, plus a FailoverMonitor whose probe/clientFor are
// injected so no real Linode API is needed.
type failoverHarness struct {
	srv     *Server
	api     *fakeAPI
	monitor *FailoverMonitor
	groupID int
}

func newFailoverHarness(t *testing.T) *failoverHarness {
	t.Helper()
	st, _, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if _, err := st.AddToken(1, "main", "pat1"); err != nil {
		t.Fatalf("AddToken pat1: %v", err)
	}
	if _, err := st.AddToken(1, "backup", "pat2"); err != nil {
		t.Fatalf("AddToken pat2: %v", err)
	}
	g, err := st.CreateAccountGroup(1, "grp")
	if err != nil {
		t.Fatalf("CreateAccountGroup: %v", err)
	}
	if err := st.AddAccountGroupMember(1, g.ID, 1); err != nil {
		t.Fatalf("AddAccountGroupMember 1: %v", err)
	}
	if err := st.AddAccountGroupMember(1, g.ID, 2); err != nil {
		t.Fatalf("AddAccountGroupMember 2: %v", err)
	}

	api := &fakeAPI{
		getInstance: func(_ context.Context, id int) (linode.Instance, error) {
			return linode.Instance{
				ID: id, Label: "web", Status: "running", Region: "us-east",
				Type: "g6-standard-1", Image: "linode/debian12", IPv4: []string{"1.2.3.4"},
			}, nil
		},
		createInstance: func(_ context.Context, opts linode.CreateOptions) (linode.Instance, error) {
			return linode.Instance{
				ID: 100, Label: opts.Label, Region: opts.Region,
				Type: opts.Type, Image: opts.Image, Status: "booting",
			}, nil
		},
	}
	srv := New(api, st)
	m := &FailoverMonitor{srv: srv, tick: time.Minute, log: slog.New(slog.DiscardHandler)}
	m.clientFor = func(string) LinodeAPI { return api }
	return &failoverHarness{srv: srv, api: api, monitor: m, groupID: g.ID}
}

// probeByPAT returns a probe that maps a PAT to a fixed status; unknown PATs
// report HealthError.
func probeByPAT(byPAT map[string]string) func(context.Context, string) (string, string) {
	return func(_ context.Context, pat string) (string, string) {
		if status, ok := byPAT[pat]; ok {
			return status, "detail-" + status
		}
		return HealthError, "unknown pat"
	}
}

func (h *failoverHarness) enable(t *testing.T, instanceID, tokenID, threshold int) {
	t.Helper()
	if err := h.srv.Store.SetFailover(store.Failover{
		InstanceID: instanceID, UserID: 1, GroupID: h.groupID, TokenID: tokenID,
		Enabled: true, Threshold: threshold, IntervalSec: 60,
	}); err != nil {
		t.Fatalf("SetFailover: %v", err)
	}
}

// TestFailoverReplacesWhenThresholdReached covers the happy path: a no_balance
// account at the threshold is replaced on the group's healthy account, the
// config is moved (with the token updated), and the boot script follows.
func TestFailoverReplacesWhenThresholdReached(t *testing.T) {
	h := newFailoverHarness(t)
	h.monitor.probe = probeByPAT(map[string]string{"pat1": HealthNoBalance, "pat2": HealthOK})
	h.enable(t, 42, 1, 1)
	if err := h.srv.Store.SetInstanceScript(42, "#!/bin/bash\necho hi"); err != nil {
		t.Fatalf("SetInstanceScript: %v", err)
	}

	h.monitor.tickOnce(context.Background())

	if _, ok := h.srv.Store.Failover(42); ok {
		t.Fatal("旧实例配置应已迁移走")
	}
	cfg, ok := h.srv.Store.Failover(100)
	if !ok {
		t.Fatal("新实例应存在替换配置")
	}
	if cfg.TokenID != 2 {
		t.Fatalf("承载账号应改为候选账号 2, got %d", cfg.TokenID)
	}
	if cfg.LastStatus != HealthOK || cfg.FailCount != 0 {
		t.Fatalf("新配置状态 = %+v, want ok/0", cfg)
	}
	if !strings.Contains(cfg.LastDetail, "backup") {
		t.Fatalf("替换详情应含候选账号名, got %q", cfg.LastDetail)
	}
	if script, ok := h.srv.Store.InstanceScript(100); !ok || !strings.Contains(script, "echo hi") {
		t.Fatalf("开机脚本应迁移到新实例, got %q ok=%v", script, ok)
	}
	if !h.api.called("CreateInstance") {
		t.Fatal("应在候选账号创建替换实例")
	}
	if !h.api.called("DeleteInstance") {
		t.Fatal("应尽力清理旧实例")
	}
}

// TestFailoverBelowThresholdAccumulates verifies a single failure below the
// threshold only bumps FailCount and does not create an instance.
func TestFailoverBelowThresholdAccumulates(t *testing.T) {
	h := newFailoverHarness(t)
	h.monitor.probe = probeByPAT(map[string]string{"pat1": HealthNoBalance})
	h.enable(t, 42, 1, 3)

	h.monitor.tickOnce(context.Background())

	cfg, _ := h.srv.Store.Failover(42)
	if cfg.FailCount != 1 || cfg.LastStatus != HealthNoBalance {
		t.Fatalf("第一次失败后 = %+v, want fail=1 no_balance", cfg)
	}
	if h.api.called("CreateInstance") {
		t.Fatal("未达阈值不应创建实例")
	}
}

// TestFailoverNoAvailableAccount verifies that when no group member is healthy
// the config is left in an error state and no instance is created.
func TestFailoverNoAvailableAccount(t *testing.T) {
	h := newFailoverHarness(t)
	h.monitor.probe = probeByPAT(map[string]string{"pat1": HealthNoBalance, "pat2": HealthBanned})
	h.enable(t, 42, 1, 1)

	h.monitor.tickOnce(context.Background())

	cfg, _ := h.srv.Store.Failover(42)
	if cfg.LastStatus != "error" || cfg.FailCount != 0 {
		t.Fatalf("无可用账号后 = %+v, want error/0", cfg)
	}
	if !strings.Contains(cfg.LastDetail, "没有可用账号") {
		t.Fatalf("详情 = %q, want 含「没有可用账号」", cfg.LastDetail)
	}
	if h.api.called("CreateInstance") {
		t.Fatal("组内无可用账号不应创建实例")
	}
}

// TestFailoverHealthyResets verifies a healthy account clears the fail count.
func TestFailoverHealthyResets(t *testing.T) {
	h := newFailoverHarness(t)
	h.monitor.probe = probeByPAT(map[string]string{"pat1": HealthOK})
	h.enable(t, 42, 1, 3)
	if err := h.srv.Store.UpdateFailoverState(42, 2, HealthNoBalance, "x", "", ""); err != nil {
		t.Fatalf("UpdateFailoverState: %v", err)
	}

	h.monitor.tickOnce(context.Background())

	cfg, _ := h.srv.Store.Failover(42)
	if cfg.FailCount != 0 || cfg.LastStatus != HealthOK {
		t.Fatalf("健康账号后 = %+v, want ok/0", cfg)
	}
}

// TestFailoverHealthErrorNotCounted verifies a probe error neither bumps the
// count nor triggers a replacement (even at the threshold).
func TestFailoverHealthErrorNotCounted(t *testing.T) {
	h := newFailoverHarness(t)
	h.monitor.probe = probeByPAT(map[string]string{"pat1": HealthError})
	h.enable(t, 42, 1, 2)
	if err := h.srv.Store.UpdateFailoverState(42, 2, HealthNoBalance, "x", "", ""); err != nil {
		t.Fatalf("UpdateFailoverState: %v", err)
	}

	h.monitor.tickOnce(context.Background())

	cfg, _ := h.srv.Store.Failover(42)
	if cfg.FailCount != 2 || cfg.LastStatus != "error" {
		t.Fatalf("检测出错后 = %+v, want error/2（不累计）", cfg)
	}
	if h.api.called("CreateInstance") {
		t.Fatal("检测出错不应触发替换")
	}
}
