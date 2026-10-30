package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"linode-panel/internal/store"
)

// failoverHTTPHarness wires a logged-in alice with three tokens: two members of
// one account group (memberID) and one outside it (outsiderID).
type failoverHTTPHarness struct {
	srv        *Server
	cookie     *http.Cookie
	groupID    int
	memberID   int
	outsiderID int
}

func newFailoverHTTPHarness(t *testing.T) *failoverHTTPHarness {
	t.Helper()
	srv := testServer(t, &fakeAPI{})
	cookie := loginAlice(t, srv)
	addAliceToken(t, srv, "main", "pat1")
	addAliceToken(t, srv, "backup", "pat2")
	addAliceToken(t, srv, "outsider", "pat3")
	g, err := srv.Store.CreateAccountGroup(aliceID, "grp")
	if err != nil {
		t.Fatalf("CreateAccountGroup: %v", err)
	}
	toks := srv.Store.TokensOf(aliceID)
	if len(toks) != 3 {
		t.Fatalf("tokens = %d, want 3", len(toks))
	}
	for _, tid := range []int{toks[0].ID, toks[1].ID} {
		if err := srv.Store.AddAccountGroupMember(aliceID, g.ID, tid); err != nil {
			t.Fatalf("AddAccountGroupMember %d: %v", tid, err)
		}
	}
	return &failoverHTTPHarness{
		srv: srv, cookie: cookie, groupID: g.ID,
		memberID: toks[0].ID, outsiderID: toks[2].ID,
	}
}

func (h *failoverHTTPHarness) put(t *testing.T, instanceID int, body string) *httptest.ResponseRecorder {
	t.Helper()
	return doReq(t, h.srv, h.cookie, http.MethodPut,
		fmt.Sprintf("/api/account/failover/%d", instanceID), body)
}

func TestFailoverHTTPListEmpty(t *testing.T) {
	h := newFailoverHTTPHarness(t)
	rr := authedGet(t, h.srv, h.cookie, "/api/account/failover")
	wantStatus(t, rr, http.StatusOK)
	if !strings.Contains(rr.Body.String(), `"failovers":[]`) {
		t.Fatalf("空结果应序列化为 [], body=%s", rr.Body.String())
	}
	var got struct {
		Failovers []store.Failover `json:"failovers"`
	}
	decodeJSON(t, rr, &got)
	if len(got.Failovers) != 0 {
		t.Fatalf("空列表应无元素, got %+v", got.Failovers)
	}
}

func TestFailoverHTTPPutValidation(t *testing.T) {
	h := newFailoverHTTPHarness(t)

	// 非法实例 ID（路径参数）。
	rr := doReq(t, h.srv, h.cookie, http.MethodPut, "/api/account/failover/abc", `{}`)
	wantStatus(t, rr, http.StatusBadRequest)
	rr = doReq(t, h.srv, h.cookie, http.MethodPut, "/api/account/failover/0", `{}`)
	wantStatus(t, rr, http.StatusBadRequest)

	// 账号 ID 无效。
	rr = h.put(t, 42, `{"token_id":0,"group_id":1}`)
	wantStatus(t, rr, http.StatusBadRequest)
	if !strings.Contains(rr.Body.String(), "账号 ID 无效") {
		t.Fatalf("body=%s", rr.Body.String())
	}

	// 未选择账号组。
	rr = h.put(t, 42, fmt.Sprintf(`{"token_id":%d,"group_id":0}`, h.memberID))
	wantStatus(t, rr, http.StatusBadRequest)
	if !strings.Contains(rr.Body.String(), "请选择账号组") {
		t.Fatalf("body=%s", rr.Body.String())
	}

	// 账号组不存在。
	rr = h.put(t, 42, fmt.Sprintf(`{"token_id":%d,"group_id":99999}`, h.memberID))
	wantStatus(t, rr, http.StatusBadRequest)

	// 账号不在所选组内。
	rr = h.put(t, 42, fmt.Sprintf(`{"token_id":%d,"group_id":%d}`, h.outsiderID, h.groupID))
	wantStatus(t, rr, http.StatusBadRequest)
	if !strings.Contains(rr.Body.String(), "不在所选账号组内") {
		t.Fatalf("body=%s", rr.Body.String())
	}
}

func TestFailoverHTTPPutAndList(t *testing.T) {
	h := newFailoverHTTPHarness(t)

	// 缺省 threshold / interval 使用默认值。
	body := fmt.Sprintf(`{"token_id":%d,"group_id":%d,"enabled":true}`, h.memberID, h.groupID)
	rr := h.put(t, 42, body)
	wantStatus(t, rr, http.StatusOK)
	var ok okResponse
	decodeJSON(t, rr, &ok)
	if !ok.OK {
		t.Fatal("PUT 应返回 ok:true")
	}
	cfg, found := h.srv.Store.Failover(42)
	if !found {
		t.Fatal("配置应已落库")
	}
	if cfg.UserID != aliceID || cfg.TokenID != h.memberID || cfg.GroupID != h.groupID || !cfg.Enabled {
		t.Fatalf("落库内容 = %+v", cfg)
	}
	if cfg.Threshold != DefaultFailoverThreshold {
		t.Fatalf("threshold 默认值 = %d, want %d", cfg.Threshold, DefaultFailoverThreshold)
	}
	if cfg.IntervalSec != int(DefaultFailoverInterval/time.Second) {
		t.Fatalf("interval 默认值 = %d", cfg.IntervalSec)
	}

	// GET 回读。
	rr = authedGet(t, h.srv, h.cookie, "/api/account/failover")
	wantStatus(t, rr, http.StatusOK)
	var got struct {
		Failovers []store.Failover `json:"failovers"`
	}
	decodeJSON(t, rr, &got)
	if len(got.Failovers) != 1 || got.Failovers[0].InstanceID != 42 {
		t.Fatalf("GET = %+v", got.Failovers)
	}
}

func TestFailoverHTTPDeleteIdempotent(t *testing.T) {
	h := newFailoverHTTPHarness(t)
	rr := h.put(t, 42, fmt.Sprintf(`{"token_id":%d,"group_id":%d,"enabled":true,"threshold":2,"interval_seconds":120}`, h.memberID, h.groupID))
	wantStatus(t, rr, http.StatusOK)

	if rr := authedDelete(t, h.srv, h.cookie, "/api/account/failover/42"); rr.Code != http.StatusOK {
		t.Fatalf("delete status = %d", rr.Code)
	}
	if _, found := h.srv.Store.Failover(42); found {
		t.Fatal("配置应已删除")
	}
	// 再次删除仍成功（幂等）。
	if rr := authedDelete(t, h.srv, h.cookie, "/api/account/failover/42"); rr.Code != http.StatusOK {
		t.Fatalf("second delete status = %d", rr.Code)
	}
}

func TestFailoverHTTPCrossUserFiltered(t *testing.T) {
	h := newFailoverHTTPHarness(t)
	// alice 自己的配置。
	rr := h.put(t, 42, fmt.Sprintf(`{"token_id":%d,"group_id":%d,"enabled":true}`, h.memberID, h.groupID))
	wantStatus(t, rr, http.StatusOK)
	// 另一个用户（管理员 id 1）的配置，直接落库以模拟他人数据。
	if err := h.srv.Store.SetFailover(store.Failover{
		InstanceID: 77, UserID: 1, GroupID: 1, TokenID: 1,
		Enabled: true, Threshold: 1, IntervalSec: 60,
	}); err != nil {
		t.Fatalf("seed other user: %v", err)
	}

	rr = authedGet(t, h.srv, h.cookie, "/api/account/failover")
	wantStatus(t, rr, http.StatusOK)
	var got struct {
		Failovers []store.Failover `json:"failovers"`
	}
	decodeJSON(t, rr, &got)
	if len(got.Failovers) != 1 || got.Failovers[0].InstanceID != 42 {
		t.Fatalf("GET 应只返回本人配置, got %+v", got.Failovers)
	}
}
