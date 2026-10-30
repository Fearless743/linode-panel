package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"linode-panel/internal/store"
)

// createAccountGroupForTest creates a group through the HTTP API and fails the
// test when the response is not a 201 with a usable AccountGroup.
func createAccountGroupForTest(t *testing.T, srv *Server, cookie *http.Cookie, name string) store.AccountGroup {
	t.Helper()
	rr := authedPost(t, srv, cookie, "/api/account/groups", `{"name":"`+name+`"}`)
	wantStatus(t, rr, http.StatusCreated)
	var g store.AccountGroup
	decodeJSON(t, rr, &g)
	if g.ID <= 0 {
		t.Fatalf("created group id = %d, body=%s", g.ID, rr.Body.String())
	}
	return g
}

// findAccountGroup returns the group with id gid from a list response, or nil.
func findAccountGroup(groups []store.AccountGroup, gid int) *store.AccountGroup {
	for i := range groups {
		if groups[i].ID == gid {
			return &groups[i]
		}
	}
	return nil
}

// TestAccountGroupHTTP covers the current user's account-group endpoints and
// their members.
func TestAccountGroupHTTP(t *testing.T) {
	srv := testServer(t, &fakeAPI{})
	session := loginAlice(t, srv)
	addAliceToken(t, srv, "main", "linode_pat_main")
	addAliceToken(t, srv, "backup", "linode_pat_backup")
	toks := srv.Store.TokensOf(aliceID)
	if len(toks) != 2 {
		t.Fatalf("tokens = %+v, want 2", toks)
	}
	tid1, tid2 := toks[0].ID, toks[1].ID

	// 初始列表为空，且必须是 [] 而不是 null。
	rr := authedGet(t, srv, session, "/api/account/groups")
	wantStatus(t, rr, http.StatusOK)
	var list struct {
		Groups []store.AccountGroup `json:"groups"`
	}
	decodeJSON(t, rr, &list)
	if list.Groups == nil || len(list.Groups) != 0 {
		t.Fatalf("initial groups = %#v, want empty array", list.Groups)
	}

	// 建组：201 直接返回组对象。
	g := createAccountGroupForTest(t, srv, session, "prod")
	if g.Name != "prod" || g.UserID != aliceID || g.TokenIDs == nil || len(g.TokenIDs) != 0 {
		t.Fatalf("created group = %+v", g)
	}

	// 重名与空名：400，并透传 store 的中文错误。
	rr = authedPost(t, srv, session, "/api/account/groups", `{"name":"prod"}`)
	wantStatus(t, rr, http.StatusBadRequest)
	var e errorResponse
	decodeJSON(t, rr, &e)
	if !strings.Contains(e.Error, "已存在") {
		t.Fatalf("duplicate group error = %q", e.Error)
	}
	rr = authedPost(t, srv, session, "/api/account/groups", `{"name":"   "}`)
	wantStatus(t, rr, http.StatusBadRequest)
	decodeJSON(t, rr, &e)
	if e.Error != "组名不能为空" {
		t.Fatalf("empty group error = %q", e.Error)
	}
	rr = authedPost(t, srv, session, "/api/account/groups", `not-json`)
	wantStatus(t, rr, http.StatusBadRequest)
	decodeJSON(t, rr, &e)
	if e.Error != "请求体无效" {
		t.Fatalf("bad body error = %q", e.Error)
	}

	// 改名。
	groupPath := "/api/account/groups/" + strconv.Itoa(g.ID)
	rr = authedPut(t, srv, session, groupPath, `{"name":"prod-eu"}`)
	wantStatus(t, rr, http.StatusOK)
	var ok okResponse
	decodeJSON(t, rr, &ok)
	if !ok.OK {
		t.Fatalf("rename response = %s", rr.Body.String())
	}
	if got, found := srv.Store.AccountGroup(aliceID, g.ID); !found || got.Name != "prod-eu" {
		t.Fatalf("after rename = %+v, %v", got, found)
	}
	rr = authedPut(t, srv, session, groupPath, `{"name":" "}`)
	wantStatus(t, rr, http.StatusBadRequest)
	other := createAccountGroupForTest(t, srv, session, "other")
	rr = authedPut(t, srv, session, groupPath, `{"name":"other"}`)
	wantStatus(t, rr, http.StatusBadRequest)
	decodeJSON(t, rr, &e)
	if !strings.Contains(e.Error, "已存在") {
		t.Fatalf("rename duplicate error = %q", e.Error)
	}

	// 加成员：先加 tid1，重复添加幂等。
	rr = authedPost(t, srv, session, groupPath+"/tokens", fmt.Sprintf(`{"token_id":%d}`, tid1))
	wantStatus(t, rr, http.StatusOK)
	rr = authedPost(t, srv, session, groupPath+"/tokens", fmt.Sprintf(`{"token_id":%d}`, tid1))
	wantStatus(t, rr, http.StatusOK)
	rr = authedGet(t, srv, session, "/api/account/groups")
	wantStatus(t, rr, http.StatusOK)
	decodeJSON(t, rr, &list)
	if got := findAccountGroup(list.Groups, g.ID); got == nil || len(got.TokenIDs) != 1 || got.TokenIDs[0] != tid1 {
		t.Fatalf("after add member = %+v", list.Groups)
	}

	// 第二个成员按升序出现在 token_ids 中。
	rr = authedPost(t, srv, session, groupPath+"/tokens", fmt.Sprintf(`{"token_id":%d}`, tid2))
	wantStatus(t, rr, http.StatusOK)
	rr = authedGet(t, srv, session, "/api/account/groups")
	decodeJSON(t, rr, &list)
	if got := findAccountGroup(list.Groups, g.ID); got == nil || len(got.TokenIDs) != 2 {
		t.Fatalf("after second member = %+v", list.Groups)
	} else if !(got.TokenIDs[0] < got.TokenIDs[1]) {
		t.Fatalf("token_ids not sorted: %+v", got.TokenIDs)
	}

	// 非法/不存在的 token：400。
	rr = authedPost(t, srv, session, groupPath+"/tokens", `{"token_id":0}`)
	wantStatus(t, rr, http.StatusBadRequest)
	decodeJSON(t, rr, &e)
	if e.Error != "令牌 ID 无效" {
		t.Fatalf("token id 0 error = %q", e.Error)
	}
	rr = authedPost(t, srv, session, groupPath+"/tokens", `{"token_id":9999}`)
	wantStatus(t, rr, http.StatusBadRequest)
	decodeJSON(t, rr, &e)
	if !strings.Contains(e.Error, "不存在") {
		t.Fatalf("missing token error = %q", e.Error)
	}

	// 移成员（移除非成员也幂等）。
	rr = authedDelete(t, srv, session, groupPath+"/tokens/"+strconv.Itoa(tid1))
	wantStatus(t, rr, http.StatusOK)
	rr = authedDelete(t, srv, session, groupPath+"/tokens/"+strconv.Itoa(tid1))
	wantStatus(t, rr, http.StatusOK)
	rr = authedGet(t, srv, session, "/api/account/groups")
	decodeJSON(t, rr, &list)
	if got := findAccountGroup(list.Groups, g.ID); got == nil || len(got.TokenIDs) != 1 || got.TokenIDs[0] != tid2 {
		t.Fatalf("after remove member = %+v", list.Groups)
	}

	// 非法 id：400，错误信息与现有风格一致。
	for _, tc := range []struct {
		method, path, body, wantErr string
	}{
		{http.MethodPut, "/api/account/groups/abc", `{"name":"x"}`, "组 ID 无效"},
		{http.MethodDelete, "/api/account/groups/0", "", "组 ID 无效"},
		{http.MethodPost, "/api/account/groups/abc/tokens", `{"token_id":1}`, "组 ID 无效"},
		{http.MethodDelete, groupPath + "/tokens/abc", "", "令牌 ID 无效"},
		{http.MethodDelete, groupPath + "/tokens/0", "", "令牌 ID 无效"},
	} {
		rr = doReq(t, srv, session, tc.method, tc.path, tc.body)
		wantStatus(t, rr, http.StatusBadRequest)
		decodeJSON(t, rr, &e)
		if e.Error != tc.wantErr {
			t.Fatalf("%s %s error = %q, want %q", tc.method, tc.path, e.Error, tc.wantErr)
		}
	}

	// 删组：成员行一并删除，重复删返回 400。
	rr = authedDelete(t, srv, session, groupPath)
	wantStatus(t, rr, http.StatusOK)
	if _, found := srv.Store.AccountGroup(aliceID, g.ID); found {
		t.Fatalf("group %d still exists after delete", g.ID)
	}
	rr = authedDelete(t, srv, session, groupPath)
	wantStatus(t, rr, http.StatusBadRequest)
	decodeJSON(t, rr, &e)
	if !strings.Contains(e.Error, "不存在") {
		t.Fatalf("delete missing group error = %q", e.Error)
	}
	rr = authedGet(t, srv, session, "/api/account/groups")
	decodeJSON(t, rr, &list)
	if len(list.Groups) != 1 || list.Groups[0].ID != other.ID {
		t.Fatalf("groups after delete = %+v", list.Groups)
	}
}

// TestAccountGroupEmptyTokenIDsSerializesAsArray guards the frozen API
// contract that token_ids is always a JSON array, even for a brand-new group
// with no members (the store returns nil for it).
func TestAccountGroupEmptyTokenIDsSerializesAsArray(t *testing.T) {
	srv := testServer(t, &fakeAPI{})
	session := loginAlice(t, srv)

	// 建组响应：token_ids 必须是 []，不能是 null。
	rr := authedPost(t, srv, session, "/api/account/groups", `{"name":"empty-box"}`)
	wantStatus(t, rr, http.StatusCreated)
	if body := rr.Body.String(); !strings.Contains(body, `"token_ids":[]`) {
		t.Fatalf("create body token_ids is not []: %s", body)
	}
	var g store.AccountGroup
	decodeJSON(t, rr, &g)

	// 列组响应：该组尚未加任何成员，token_ids 同样必须是 []。
	rr = authedGet(t, srv, session, "/api/account/groups")
	wantStatus(t, rr, http.StatusOK)
	if body := rr.Body.String(); !strings.Contains(body, `"token_ids":[]`) {
		t.Fatalf("list body token_ids is not []: %s", body)
	}
	var list struct {
		Groups []store.AccountGroup `json:"groups"`
	}
	decodeJSON(t, rr, &list)
	if len(list.Groups) != 1 || list.Groups[0].ID != g.ID || list.Groups[0].TokenIDs == nil {
		t.Fatalf("empty group list = %+v", list.Groups)
	}
}

// TestAccountGroupCrossUserIsolation ensures one user can neither see nor
// modify another user's account groups or add another user's tokens.
func TestAccountGroupCrossUserIsolation(t *testing.T) {
	srv := testServer(t, &fakeAPI{})
	aliceSession := loginAlice(t, srv)
	addAliceToken(t, srv, "alice-main", "linode_pat_alice")

	aliceToks := srv.Store.TokensOf(aliceID)
	if len(aliceToks) != 1 {
		t.Fatalf("alice tokens = %+v", aliceToks)
	}
	aliceTid := aliceToks[0].ID
	g := createAccountGroupForTest(t, srv, aliceSession, "shared")
	groupPath := "/api/account/groups/" + strconv.Itoa(g.ID)

	bob, err := srv.Store.CreateUser("bob", "bob123", 1)
	if err != nil {
		t.Fatalf("CreateUser bob: %v", err)
	}
	bobToken, err := srv.Store.AddToken(bob.ID, "bob-main", "linode_pat_bob")
	if err != nil {
		t.Fatalf("AddToken bob: %v", err)
	}
	bobSession := loginAs(t, srv, "bob", "bob123")

	// bob 看不到 alice 的组。
	rr := authedGet(t, srv, bobSession, "/api/account/groups")
	wantStatus(t, rr, http.StatusOK)
	var list struct {
		Groups []store.AccountGroup `json:"groups"`
	}
	decodeJSON(t, rr, &list)
	if len(list.Groups) != 0 {
		t.Fatalf("bob sees groups = %+v, want none", list.Groups)
	}

	// bob 改/删/加成员都被当作组不存在，400。
	var e errorResponse
	rr = authedPut(t, srv, bobSession, groupPath, `{"name":"hijack"}`)
	wantStatus(t, rr, http.StatusBadRequest)
	decodeJSON(t, rr, &e)
	if !strings.Contains(e.Error, "不存在") {
		t.Fatalf("bob rename alice group error = %q", e.Error)
	}
	rr = authedDelete(t, srv, bobSession, groupPath)
	wantStatus(t, rr, http.StatusBadRequest)
	decodeJSON(t, rr, &e)
	if !strings.Contains(e.Error, "不存在") {
		t.Fatalf("bob delete alice group error = %q", e.Error)
	}
	rr = authedPost(t, srv, bobSession, groupPath+"/tokens", fmt.Sprintf(`{"token_id":%d}`, bobToken.ID))
	wantStatus(t, rr, http.StatusBadRequest)
	decodeJSON(t, rr, &e)
	if !strings.Contains(e.Error, "不存在") {
		t.Fatalf("bob add member to alice group error = %q", e.Error)
	}
	rr = authedDelete(t, srv, bobSession, groupPath+"/tokens/"+strconv.Itoa(bobToken.ID))
	wantStatus(t, rr, http.StatusBadRequest)

	// alice 也不能把 bob 的 token 加进自己的组。
	rr = authedPost(t, srv, aliceSession, groupPath+"/tokens", fmt.Sprintf(`{"token_id":%d}`, bobToken.ID))
	wantStatus(t, rr, http.StatusBadRequest)
	decodeJSON(t, rr, &e)
	if !strings.Contains(e.Error, "不存在") {
		t.Fatalf("alice add bob token error = %q", e.Error)
	}

	// alice 的组仍完好、无成员；alice 的 token 不受影响。
	got, found := srv.Store.AccountGroup(aliceID, g.ID)
	if !found || got.Name != "shared" || len(got.TokenIDs) != 0 {
		t.Fatalf("alice group after bob attempts = %+v, %v", got, found)
	}
	if toks := srv.Store.TokensOf(aliceID); len(toks) != 1 || toks[0].ID != aliceTid {
		t.Fatalf("alice tokens after bob attempts = %+v", toks)
	}
}
