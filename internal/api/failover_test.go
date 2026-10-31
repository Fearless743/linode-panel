package api

import (
	"context"
	"errors"
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
	srv        *Server
	api        *fakeAPI
	monitor    *FailoverMonitor
	groupID    int
	createOpts *[]linode.CreateOptions
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

	var createOpts []linode.CreateOptions
	api := &fakeAPI{
		getInstance: func(_ context.Context, id int) (linode.Instance, error) {
			return linode.Instance{
				ID: id, Label: "web", Status: "running", Region: "us-east",
				Type: "g6-standard-1", Image: "linode/debian12", IPv4: []string{"1.2.3.4"},
			}, nil
		},
		createInstance: func(_ context.Context, opts linode.CreateOptions) (linode.Instance, error) {
			createOpts = append(createOpts, opts)
			return linode.Instance{
				ID: 100, Label: opts.Label, Region: opts.Region,
				Type: opts.Type, Image: opts.Image, Status: "booting",
			}, nil
		},
	}
	srv := New(api, st)
	m := &FailoverMonitor{srv: srv, tick: time.Minute, log: slog.New(slog.DiscardHandler)}
	m.clientFor = func(string) LinodeAPI { return api }
	return &failoverHarness{srv: srv, api: api, monitor: m, groupID: g.ID, createOpts: &createOpts}
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

// TestFailoverReplacesWhenThresholdReached covers the happy path: a banned
// account at the threshold is replaced on the group's healthy account, the
// config is moved (with the token updated), and the boot script follows.
func TestFailoverReplacesWhenThresholdReached(t *testing.T) {
	h := newFailoverHarness(t)
	h.monitor.probe = probeByPAT(map[string]string{"pat1": HealthBanned, "pat2": HealthOK})
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
	// 替换实例必须沿用旧实例的相同配置，且开机启动。
	if len(*h.createOpts) != 1 {
		t.Fatalf("CreateInstance 调用次数 = %d, want 1", len(*h.createOpts))
	}
	opts := (*h.createOpts)[0]
	if opts.Region != "us-east" || opts.Type != "g6-standard-1" ||
		opts.Image != "linode/debian12" || opts.Label != "web" {
		t.Fatalf("CreateOptions 应与旧实例一致, got %+v", opts)
	}
	if opts.Booted == nil || !*opts.Booted {
		t.Fatalf("CreateOptions.Booted 应为 true, got %v", opts.Booted)
	}
	if !h.api.called("DeleteInstance") {
		t.Fatal("应尽力清理旧实例")
	}
}

// TestFailoverBelowThresholdAccumulates verifies a single failure below the
// threshold only bumps FailCount and does not create an instance.
func TestFailoverBelowThresholdAccumulates(t *testing.T) {
	h := newFailoverHarness(t)
	h.monitor.probe = probeByPAT(map[string]string{"pat1": HealthBanned})
	h.enable(t, 42, 1, 3)

	h.monitor.tickOnce(context.Background())

	cfg, _ := h.srv.Store.Failover(42)
	if cfg.FailCount != 1 || cfg.LastStatus != HealthBanned {
		t.Fatalf("第一次失败后 = %+v, want fail=1 banned", cfg)
	}
	if h.api.called("CreateInstance") {
		t.Fatal("未达阈值不应创建实例")
	}
}

// TestFailoverNoAvailableAccount verifies that when no group member is healthy
// the config is left in an error state and no instance is created.
func TestFailoverNoAvailableAccount(t *testing.T) {
	h := newFailoverHarness(t)
	h.monitor.probe = probeByPAT(map[string]string{"pat1": HealthBanned, "pat2": HealthBanned})
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

// TestFailoverCachedFallbackOnGetInstanceError verifies that when the old
// account is unreachable (GetInstance fails) the replacement still proceeds
// using the locally cached instance snapshot.
func TestFailoverCachedFallbackOnGetInstanceError(t *testing.T) {
	h := newFailoverHarness(t)
	h.monitor.probe = probeByPAT(map[string]string{"pat1": HealthNoBalance, "pat2": HealthOK})
	// 旧账号已不可达：读取实例时报封号错误（归类为 banned），仍应累计并替换。
	h.api.getInstance = func(context.Context, int) (linode.Instance, error) {
		return linode.Instance{}, &linode.APIError{StatusCode: 403, Reason: "account suspended"}
	}
	// 本地缓存里保留旧实例的配置，替换应回退到这里。
	if err := h.srv.Store.CacheInstances(1, []linode.Instance{{
		ID: 42, Label: "cached-web", Status: "running", Region: "eu-west",
		Type: "g6-standard-2", Image: "linode/debian11",
	}}); err != nil {
		t.Fatalf("CacheInstances: %v", err)
	}
	h.enable(t, 42, 1, 1)

	h.monitor.tickOnce(context.Background())

	cfg, found := h.srv.Store.Failover(100)
	if !found {
		t.Fatal("回退缓存后应成功创建并迁移到新实例")
	}
	if cfg.LastStatus != HealthOK || cfg.TokenID != 2 {
		t.Fatalf("新配置 = %+v, want ok/token=2", cfg)
	}
	if len(*h.createOpts) != 1 {
		t.Fatalf("CreateInstance 调用次数 = %d, want 1", len(*h.createOpts))
	}
	opts := (*h.createOpts)[0]
	if opts.Region != "eu-west" || opts.Type != "g6-standard-2" ||
		opts.Image != "linode/debian11" || opts.Label != "cached-web" {
		t.Fatalf("应使用缓存快照创建, got %+v", opts)
	}
}

// TestFailoverMissingGroup verifies a config whose group no longer exists
// stops in an error state without creating an instance.
func TestFailoverMissingGroup(t *testing.T) {
	h := newFailoverHarness(t)
	h.monitor.probe = probeByPAT(map[string]string{"pat1": HealthBanned, "pat2": HealthOK})
	if err := h.srv.Store.SetFailover(store.Failover{
		InstanceID: 42, UserID: 1, GroupID: 99999, TokenID: 1,
		Enabled: true, Threshold: 1, IntervalSec: 60,
	}); err != nil {
		t.Fatalf("SetFailover: %v", err)
	}

	h.monitor.tickOnce(context.Background())

	cfg, _ := h.srv.Store.Failover(42)
	if cfg.LastStatus != "error" || cfg.FailCount != 0 {
		t.Fatalf("组不存在后 = %+v, want error/0", cfg)
	}
	if !strings.Contains(cfg.LastDetail, "账号组不存在") {
		t.Fatalf("详情 = %q, want 含「账号组不存在」", cfg.LastDetail)
	}
	if h.api.called("CreateInstance") {
		t.Fatal("组不存在不应创建实例")
	}
}

// TestFailoverNoBalanceInstanceRunningNotCounted verifies that a no_balance
// account whose instance is still running is treated as healthy: the fail count
// is reset and no replacement happens (欠费宽限期实例仍服务中).
func TestFailoverNoBalanceInstanceRunningNotCounted(t *testing.T) {
	h := newFailoverHarness(t)
	h.monitor.probe = probeByPAT(map[string]string{"pat1": HealthNoBalance, "pat2": HealthOK})
	h.enable(t, 42, 1, 1)
	// 预置一个失败计数，验证会被归零。
	if err := h.srv.Store.UpdateFailoverState(42, 2, HealthNoBalance, "x", "", ""); err != nil {
		t.Fatalf("UpdateFailoverState: %v", err)
	}

	h.monitor.tickOnce(context.Background())

	cfg, _ := h.srv.Store.Failover(42)
	if cfg.FailCount != 0 || cfg.LastStatus != HealthOK {
		t.Fatalf("实例 running 时 = %+v, want ok/0", cfg)
	}
	if h.api.called("Boot") {
		t.Fatal("实例 running 不应尝试启动")
	}
	if h.api.called("CreateInstance") {
		t.Fatal("实例 running 不应触发替换")
	}
}

// TestFailoverNoBalanceInstanceStoppedBootsOk verifies a stopped instance that
// can be booted is considered available: no fail count, no replacement.
func TestFailoverNoBalanceInstanceStoppedBootsOk(t *testing.T) {
	h := newFailoverHarness(t)
	h.monitor.probe = probeByPAT(map[string]string{"pat1": HealthNoBalance, "pat2": HealthOK})
	h.api.getInstance = func(_ context.Context, id int) (linode.Instance, error) {
		return linode.Instance{ID: id, Status: "stopped", Region: "us-east", Type: "g6-standard-1", Image: "linode/debian12"}, nil
	}
	h.enable(t, 42, 1, 1)

	h.monitor.tickOnce(context.Background())

	if !h.api.called("Boot") {
		t.Fatal("实例 stopped 应尝试启动")
	}
	cfg, _ := h.srv.Store.Failover(42)
	if cfg.FailCount != 0 || cfg.LastStatus != HealthOK {
		t.Fatalf("实例能启动时 = %+v, want ok/0", cfg)
	}
	if h.api.called("CreateInstance") {
		t.Fatal("实例能启动不应触发替换")
	}
}

// TestFailoverNoBalanceInstanceStoppedBootFails verifies a stopped instance
// that cannot be booted is genuinely unusable: the fail count accrues and a
// replacement happens at the threshold.
func TestFailoverNoBalanceInstanceStoppedBootFails(t *testing.T) {
	h := newFailoverHarness(t)
	h.monitor.probe = probeByPAT(map[string]string{"pat1": HealthNoBalance, "pat2": HealthOK})
	h.api.getInstance = func(_ context.Context, id int) (linode.Instance, error) {
		return linode.Instance{ID: id, Status: "stopped", Region: "us-east", Type: "g6-standard-1", Image: "linode/debian12"}, nil
	}
	h.api.boot = func(context.Context, int) error { return errors.New("boot failed") }
	h.enable(t, 42, 1, 1)

	h.monitor.tickOnce(context.Background())

	if _, ok := h.srv.Store.Failover(42); ok {
		t.Fatal("实例无法启动应触发替换并迁移配置")
	}
	cfg, ok := h.srv.Store.Failover(100)
	if !ok || cfg.TokenID != 2 || cfg.LastStatus != HealthOK {
		t.Fatalf("替换后新配置 = %+v ok=%v", cfg, ok)
	}
	if !h.api.called("CreateInstance") {
		t.Fatal("实例确实不可用时应创建替换实例")
	}
}
