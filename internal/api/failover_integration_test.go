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
	"linode-panel/internal/store"
)

// ---------- 多账号假 Linode API（端到端用） ----------
//
// 这里不注入 probe/clientFor 桩，而是让 FailoverMonitor 使用真实的
// Server.probeAccountHealth 与 Server.clientFor，把整条链路真正打到
// httptest 假 API 上：HTTP 接口建数据 → 监控探测 GET /account →
// 读旧实例 → 在健康账号 POST /linode/instances → 迁移配置。

// 三个假账号的 PAT，handler 根据 Authorization: Bearer <PAT> 区分行为。
const (
	e2ePATMain   = "pat-main"   // 账号 A：GET /account 返回 403，判定为 banned
	e2ePATBackup = "pat-backup" // 账号 B：余额正常，承接替换实例
	e2ePATPoor   = "pat-poor"   // 账号 C：200 但余额超阈值，判定为 no_balance
)

// e2eLinodeAPI 是假 Linode API，按 PAT 区分账号行为并记录收到的请求。
//
// 账号 A（e2ePATMain）：
//   - GET /account → 403 Account suspended（测试中明确采用「403 判定封号」）；
//   - GET /linode/instances/{id} → 返回旧实例（供引擎复制 region/type/image/label）；
//   - POST/DELETE /linode/instances/{id}...（shutdown/delete）→ 500，用来验证
//     「旧实例清理失败不影响本次替换成功」；
//   - POST /linode/instances → 也记录但返回 403；正常流程不应调用，断言其次数为 0。
//
// 账号 B（e2ePATBackup）：
//   - GET /account → 200 且余额正常；
//   - POST /linode/instances → 200，返回一个新实例，并记录完整请求体。
//
// 账号 C（e2ePATPoor）：GET /account → 200 但欠款，用于反向用例的「不健康候选」。
type e2eLinodeAPI struct {
	ts    *httptest.Server
	old   linode.Instance // 账号 A 上待替换的旧实例
	newID int             // 账号 B 创建成功时返回的新实例 id

	mu           sync.Mutex
	createCalls  map[string][]linode.CreateOptions // pat -> 收到的创建请求体
	accountCalls map[string]int                    // pat -> GET /account 次数
	cleanupCalls map[string]int                    // pat -> 旧实例清理（shutdown/delete）尝试次数
}

func newE2ELinodeAPI(t *testing.T, old linode.Instance, newID int) *e2eLinodeAPI {
	t.Helper()
	a := &e2eLinodeAPI{
		old:          old,
		newID:        newID,
		createCalls:  map[string][]linode.CreateOptions{},
		accountCalls: map[string]int{},
		cleanupCalls: map[string]int{},
	}
	a.ts = httptest.NewServer(http.HandlerFunc(a.handle))
	t.Cleanup(a.ts.Close)
	return a
}

func (a *e2eLinodeAPI) handle(w http.ResponseWriter, r *http.Request) {
	pat := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/account":
		a.mu.Lock()
		a.accountCalls[pat]++
		a.mu.Unlock()
		switch pat {
		case e2ePATMain:
			// 账号 A：封号。ClassifyAccountError 对 403 统一归类为 banned。
			e2eWriteError(w, http.StatusForbidden, "Account suspended")
		case e2ePATBackup:
			writeJSON(w, http.StatusOK, linode.Account{Balance: 0, BalanceUninvoiced: 0})
		case e2ePATPoor:
			// 欠款超过默认阈值 0 → BalanceExhausted → no_balance。
			writeJSON(w, http.StatusOK, linode.Account{Balance: 42})
		default:
			e2eWriteError(w, http.StatusUnauthorized, "Not Authorized")
		}

	case r.Method == http.MethodGet && r.URL.Path == "/linode/instances/"+strconv.Itoa(a.old.ID):
		// 旧实例仍可从账号 A 读到配置；替换靠它复制 region/type/image/label。
		if pat != e2ePATMain {
			e2eWriteError(w, http.StatusNotFound, "not found")
			return
		}
		writeJSON(w, http.StatusOK, a.old)

	case r.Method == http.MethodPost && r.URL.Path == "/linode/instances":
		var opts linode.CreateOptions
		if err := json.NewDecoder(r.Body).Decode(&opts); err != nil {
			e2eWriteError(w, http.StatusBadRequest, "bad request body")
			return
		}
		a.mu.Lock()
		a.createCalls[pat] = append(a.createCalls[pat], opts)
		a.mu.Unlock()
		if pat != e2ePATBackup {
			// 只有健康账号能创建；故障账号即使被调用也必须失败，供断言计数。
			e2eWriteError(w, http.StatusForbidden, "Account suspended")
			return
		}
		writeJSON(w, http.StatusOK, linode.Instance{
			ID: a.newID, Label: opts.Label, Region: opts.Region,
			Type: opts.Type, Image: opts.Image, Status: "booting",
		})

	case (r.Method == http.MethodPost || r.Method == http.MethodDelete) &&
		strings.HasPrefix(r.URL.Path, "/linode/instances/"):
		// 旧实例 shutdown/delete 一律失败：替换仍应成功（尽力清理）。
		a.mu.Lock()
		a.cleanupCalls[pat]++
		a.mu.Unlock()
		e2eWriteError(w, http.StatusInternalServerError, "cleanup unavailable")

	default:
		e2eWriteError(w, http.StatusNotFound, "not found")
	}
}

// createsFor 返回某账号收到的创建实例请求体快照。
func (a *e2eLinodeAPI) createsFor(pat string) []linode.CreateOptions {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]linode.CreateOptions, len(a.createCalls[pat]))
	copy(out, a.createCalls[pat])
	return out
}

// totalCreates 返回所有账号收到的创建实例请求总数。
func (a *e2eLinodeAPI) totalCreates() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, calls := range a.createCalls {
		n += len(calls)
	}
	return n
}

func (a *e2eLinodeAPI) accountCallsFor(pat string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.accountCalls[pat]
}

func (a *e2eLinodeAPI) cleanupCallsFor(pat string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cleanupCalls[pat]
}

// e2eWriteError 写出与 Linode API 同形的错误体（errors[0].reason）。
func e2eWriteError(w http.ResponseWriter, status int, reason string) {
	writeJSON(w, status, map[string]any{
		"errors": []map[string]string{{"reason": reason}},
	})
}

// ---------- 用真实 HTTP 接口准备数据 ----------

// e2eAddToken 通过 POST /api/account/tokens 绑定一个账号，返回 token id。
func e2eAddToken(t *testing.T, srv *Server, cookie *http.Cookie, label, pat string) int {
	t.Helper()
	body := fmt.Sprintf(`{"label":%q,"token":%q}`, label, pat)
	rr := authedPost(t, srv, cookie, "/api/account/tokens", body)
	wantStatus(t, rr, http.StatusOK)
	var tok tokenJSON
	decodeJSON(t, rr, &tok)
	if tok.ID <= 0 {
		t.Fatalf("添加账号 %s 未返回有效 id: %+v", label, tok)
	}
	return tok.ID
}

// e2eCreateGroup 通过 POST /api/account/groups 建组并逐个加入成员，返回组 id。
func e2eCreateGroup(t *testing.T, srv *Server, cookie *http.Cookie, name string, tokenIDs ...int) int {
	t.Helper()
	rr := authedPost(t, srv, cookie, "/api/account/groups", fmt.Sprintf(`{"name":%q}`, name))
	wantStatus(t, rr, http.StatusCreated)
	var g struct {
		ID int `json:"id"`
	}
	decodeJSON(t, rr, &g)
	if g.ID <= 0 {
		t.Fatalf("建组 %s 未返回有效 id: %+v", name, g)
	}
	for _, tid := range tokenIDs {
		rr := authedPost(t, srv, cookie,
			fmt.Sprintf("/api/account/groups/%d/tokens", g.ID),
			fmt.Sprintf(`{"token_id":%d}`, tid))
		wantStatus(t, rr, http.StatusOK)
	}
	return g.ID
}

// e2eEnableFailover 通过 PUT /api/account/failover/{iid} 开启某实例的自动替换。
func e2eEnableFailover(t *testing.T, srv *Server, cookie *http.Cookie, iid, tokenID, groupID, threshold int) {
	t.Helper()
	body := fmt.Sprintf(
		`{"token_id":%d,"group_id":%d,"enabled":true,"threshold":%d,"interval_seconds":60}`,
		tokenID, groupID, threshold)
	rr := authedPut(t, srv, cookie, fmt.Sprintf("/api/account/failover/%d", iid), body)
	wantStatus(t, rr, http.StatusOK)
}

// boolPtr returns a pointer to the given bool value.
func boolPtr(v bool) *bool { return &v }

// e2eSetGroupInstanceConfig 通过 PUT /api/account/groups/{gid}/instance-config
// 设置账号组的目标开机配置。
func e2eSetGroupInstanceConfig(t *testing.T, srv *Server, cookie *http.Cookie, groupID int, cfg *store.InstanceConfigJSON) {
	t.Helper()
	booted := true
	if cfg.Booted != nil {
		booted = *cfg.Booted
	}
	body := fmt.Sprintf(
		`{"config":{"region":"%s","type":"%s","image":"%s","label":"%s","script":%q,"booted":%v}}`,
		cfg.Region, cfg.Type, cfg.Image, cfg.Label, cfg.Script, booted)
	rr := authedPut(t, srv, cookie,
		fmt.Sprintf("/api/account/groups/%d/instance-config", groupID), body)
	wantStatus(t, rr, http.StatusOK)
}

// newE2EFailoverMonitor 构造一个不启动 goroutine 的替换监控：probe/clientFor
// 都指向 Server 的真实实现，因此探测与创建都会实际请求 srv.APIBase。
func newE2EFailoverMonitor(srv *Server) *FailoverMonitor {
	m := &FailoverMonitor{srv: srv, tick: time.Hour, log: slog.New(slog.DiscardHandler)}
	m.probe = srv.probeAccountHealth
	m.clientFor = func(pat string) LinodeAPI { return srv.clientFor(pat) }
	return m
}

// ---------- 正向：故障账号被组内健康账号替换 ----------

// TestFailoverE2EReplacesBannedAccount 走完整链路：HTTP 接口添加账号 A/B、
// 建组、把 A 上实例 42 开启替换（threshold=1），再用真实探测/客户端跑一轮
// tickOnce，断言 B 收到与组配置一致的创建请求、配置迁移且状态为 ok，
// 同时 A 从未被用于创建、旧实例清理失败也不影响替换。
func TestFailoverE2EReplacesBannedAccount(t *testing.T) {
	// 组预填目标开机配置：region/type/image/label 是替换所用的字段。
	groupCfg := &store.InstanceConfigJSON{
		Region: "us-east",
		Type:   "g6-standard-1",
		Image:  "linode/debian12",
		Label:  "web-1",
		Script: "#!/bin/bash\necho e2e",
		Booted: boolPtr(true),
	}
	api := newE2ELinodeAPI(t, linode.Instance{
		ID: 42, Label: "web-1", Status: "running", Region: "us-east",
		Type: "g6-standard-1", Image: "linode/debian12", IPv4: []string{"192.0.2.10"},
	}, 200)
	srv := testServer(t, &fakeAPI{})
	srv.APIBase = api.ts.URL
	cookie := loginAlice(t, srv)

	// 端到端准备：账号 → 组 → 组员 → 目标开机配置 → 实例替换配置，全部走真实 HTTP 接口。
	tokA := e2eAddToken(t, srv, cookie, "main", e2ePATMain)
	tokB := e2eAddToken(t, srv, cookie, "backup", e2ePATBackup)
	gid := e2eCreateGroup(t, srv, cookie, "grp", tokA, tokB)
	e2eSetGroupInstanceConfig(t, srv, cookie, gid, groupCfg)

	e2eEnableFailover(t, srv, cookie, 42, tokA, gid, 1)

	// 一轮监控：账号 A 探测为 banned（403），达到阈值 1 后替换到账号 B。
	newE2EFailoverMonitor(srv).tickOnce(context.Background())

	// 1) 旧实例配置必须已迁移走。
	if _, ok := srv.Store.Failover(42); ok {
		t.Fatalf("旧实例 42 的替换配置应已迁移")
	}

	// 2) 新实例上存在配置：承载账号改为 B、Enabled 保留、状态 ok、失败计数归零。
	cfg, ok := srv.Store.Failover(200)
	if !ok {
		t.Fatal("新实例 200 上应存在替换配置")
	}
	if cfg.TokenID != tokB {
		t.Fatalf("承载账号应为账号 B(token %d), got %d", tokB, cfg.TokenID)
	}
	if !cfg.Enabled {
		t.Fatalf("Enabled 应保留, got %+v", cfg)
	}
	if cfg.LastStatus != HealthOK || cfg.FailCount != 0 {
		t.Fatalf("新配置状态应为 ok/0, got status=%q fail=%d", cfg.LastStatus, cfg.FailCount)
	}
	if cfg.GroupID != gid {
		t.Fatalf("GroupID 应保留为 %d, got %d", gid, cfg.GroupID)
	}

	// 3) 账号 B 恰好收到一次创建请求，且 region/type/image/label 与组配置一致。
	createsB := api.createsFor(e2ePATBackup)
	if len(createsB) != 1 {
		t.Fatalf("账号 B 应收到 1 次创建请求, got %d", len(createsB))
	}
	got := createsB[0]
	if got.Region != groupCfg.Region || got.Type != groupCfg.Type ||
		got.Image != groupCfg.Image || got.Label != groupCfg.Label {
		t.Fatalf("创建请求应与组配置一致: got %+v, want region=%q type=%q image=%q label=%q",
			got, groupCfg.Region, groupCfg.Type, groupCfg.Image, groupCfg.Label)
	}
	if got.Booted == nil || !*got.Booted {
		t.Fatalf("替换实例应带 booted=true, got %+v", got.Booted)
	}

	// 4) 组配置里的 script 应写入新实例。
	script, ok := srv.Store.InstanceScript(200)
	if !ok || !strings.Contains(script, "echo e2e") {
		t.Fatalf("新实例 200 应有组配置的开机脚本, got %q ok=%v", script, ok)
	}

	// 5) 账号 A 从未被用于创建实例。
	if n := len(api.createsFor(e2ePATMain)); n != 0 {
		t.Fatalf("故障账号 A 不应收到创建请求, got %d", n)
	}

	// 6) 两个账号都真正被探测过；A 的清理请求发出且失败，但替换仍然成功。
	if api.accountCallsFor(e2ePATMain) == 0 || api.accountCallsFor(e2ePATBackup) == 0 {
		t.Fatal("两个账号都应经过 GET /account 健康探测")
	}
	if api.cleanupCallsFor(e2ePATMain) == 0 {
		t.Fatal("应尽力请求清理旧实例（shutdown/delete）")
	}
}

// ---------- 反向：组内没有其他健康账号则拒绝替换 ----------

// TestFailoverE2ENoHealthyAccountKeepsConfig 断言组内除故障账号外只剩一个
// 余额不足的账号时：不产生任何创建请求，配置仍留在旧实例上且状态为 error。
func TestFailoverE2ENoHealthyAccountKeepsConfig(t *testing.T) {
	old := linode.Instance{
		ID: 42, Label: "web-1", Status: "running", Region: "us-east",
		Type: "g6-standard-1", Image: "linode/debian12", IPv4: []string{"192.0.2.10"},
	}
	api := newE2ELinodeAPI(t, old, 200)
	srv := testServer(t, &fakeAPI{})
	srv.APIBase = api.ts.URL
	cookie := loginAlice(t, srv)

	// 组内只有账号 A（封号）与账号 C（欠费），没有健康账号。
	tokA := e2eAddToken(t, srv, cookie, "main", e2ePATMain)
	tokC := e2eAddToken(t, srv, cookie, "poor", e2ePATPoor)
	gid := e2eCreateGroup(t, srv, cookie, "grp", tokA, tokC)

	// 设置组配置（这样替换流程能通过配置检查，到达无可用账号分支）。
	e2eSetGroupInstanceConfig(t, srv, cookie, gid, &store.InstanceConfigJSON{
		Region: "us-east",
		Type:   "g6-standard-1",
		Image:  "linode/debian12",
		Label:  "web-1",
		Booted: boolPtr(true),
	})

	e2eEnableFailover(t, srv, cookie, old.ID, tokA, gid, 1)

	newE2EFailoverMonitor(srv).tickOnce(context.Background())

	// 配置仍留在旧实例上，状态为 error（「组内没有可用账号，等待下一轮」）。
	cfg, ok := srv.Store.Failover(old.ID)
	if !ok {
		t.Fatal("无可用账号时配置不应被迁移走")
	}
	if cfg.LastStatus != "error" || cfg.FailCount != 0 {
		t.Fatalf("无可用账号后应为 error/0, got status=%q fail=%d", cfg.LastStatus, cfg.FailCount)
	}
	if !strings.Contains(cfg.LastDetail, "没有可用账号") {
		t.Fatalf("详情应说明没有可用账号, got %q", cfg.LastDetail)
	}

	// 任何账号都不应收到创建请求。
	if n := api.totalCreates(); n != 0 {
		t.Fatalf("无可用账号时不应创建实例, got %d 次", n)
	}

	// 故障账号与欠费账号都确实被探测过（证明不是「没跑」）。
	if api.accountCallsFor(e2ePATMain) == 0 || api.accountCallsFor(e2ePATPoor) == 0 {
		t.Fatal("故障账号与候选账号都应经过健康探测")
	}
}

// TestFailoverE2ENoGroupConfigNoCreate 验证组未配置目标开机配置时：
// 即使有健康候选账号，replace 也不建机，状态记 error。
func TestFailoverE2ENoGroupConfigNoCreate(t *testing.T) {
	old := linode.Instance{
		ID: 42, Label: "web-1", Status: "running", Region: "us-east",
		Type: "g6-standard-1", Image: "linode/debian12", IPv4: []string{"192.0.2.10"},
	}
	api := newE2ELinodeAPI(t, old, 200)
	srv := testServer(t, &fakeAPI{})
	srv.APIBase = api.ts.URL
	cookie := loginAlice(t, srv)

	tokA := e2eAddToken(t, srv, cookie, "main", e2ePATMain)
	tokB := e2eAddToken(t, srv, cookie, "backup", e2ePATBackup)
	gid := e2eCreateGroup(t, srv, cookie, "grp", tokA, tokB)
	e2eEnableFailover(t, srv, cookie, old.ID, tokA, gid, 1)
	// 注意：未设置组配置——严格模式下 replace 应直接报 error。

	newE2EFailoverMonitor(srv).tickOnce(context.Background())

	cfg, ok := srv.Store.Failover(old.ID)
	if !ok {
		t.Fatal("未配置组目标开机配置时配置不应被迁移走")
	}
	if cfg.LastStatus != "error" || cfg.FailCount != 0 {
		t.Fatalf("应为 error/0, got status=%q fail=%d", cfg.LastStatus, cfg.FailCount)
	}
	if !strings.Contains(cfg.LastDetail, "未填写账号组目标开机配置") {
		t.Fatalf("详情应含「未填写账号组目标开机配置」, got %q", cfg.LastDetail)
	}
	if n := api.totalCreates(); n != 0 {
		t.Fatalf("未配置时不应创建实例, got %d 次", n)
	}
}
