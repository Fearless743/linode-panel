package api

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"linode-panel/internal/linode"
)

// accountHealthAPI mocks GET /account per PAT, returning different balances or
// errors so the health snapshot in handleListMyTokens can be verified.
func accountHealthAPI(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/account" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"errors":[{"reason":"not found"}]}`)
			return
		}
		pat := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		switch pat {
		case "pat_ok":
			_, _ = io.WriteString(w, `{"balance":-1.5,"balance_uninvoiced":0}`)
		case "pat_nobalance":
			_, _ = io.WriteString(w, `{"balance":2.0,"balance_uninvoiced":0}`)
		case "pat_banned":
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"errors":[{"reason":"Account suspended"}]}`)
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"errors":[{"reason":"internal"}]}`)
		}
	}))
}

// mockLinodeAPI serves minimal /regions /linode/types /images and
// POST /linode/instances responses for clientFor-backed handlers, plus the
// Cloud Firewall endpoints so the default allow-all attach succeeds.
func mockLinodeAPI(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/linode/instances":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":100,"label":"myinst","status":"offline","region":"us-east","type":"g6-standard-1","image":"linode/debian12","ipv4":[],"ipv6":""}`)
		case r.Method == http.MethodGet && r.URL.Path == "/networking/firewalls":
			_, _ = io.WriteString(w, `{"data":[],"page":1,"pages":1}`)
		case r.Method == http.MethodPost && r.URL.Path == "/networking/firewalls":
			_, _ = io.WriteString(w, `{"id":5,"label":"linode-panel-allow-all","region":"us-east","status":"enabled","tags":["linode-panel-allow-all"]}`)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/networking/firewalls/") && strings.HasSuffix(r.URL.Path, "/devices"):
			_, _ = io.WriteString(w, `{"data":[],"page":1,"pages":1}`)
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/networking/firewalls/") && strings.HasSuffix(r.URL.Path, "/devices"):
			_, _ = io.WriteString(w, `{"id":1,"entity":{"id":100,"type":"linode","label":"myinst"}}`)
		case r.URL.Path == "/regions":
			// 故意乱序返回，验证 handler 按 country 排序（同国家内按 id）。
			_, _ = io.WriteString(w, `{"data":[{"id":"us-west","label":"Fremont","country":"us"},{"id":"de-fra-2","label":"Frankfurt 2","country":"de"},{"id":"us-east","label":"Newark","country":"us"},{"id":"eu-central","label":"Frankfurt","country":"de"}],"page":1,"pages":1}`)
		case r.URL.Path == "/linode/types":
			_, _ = io.WriteString(w, `{"data":[{"id":"g6-standard-4","label":"Linode 8GB"},{"id":"g6-standard-1","label":"Linode 2GB"},{"id":"g6-standard-2","label":"Linode 4GB"}],"page":1,"pages":1}`)
		case r.URL.Path == "/images":
			_, _ = io.WriteString(w, `{"data":[{"id":"linode/ubuntu24.04","label":"Ubuntu 24.04"},{"id":"linode/debian12","label":"Debian 12"},{"id":"linode/ubuntu22.04","label":"Ubuntu 22.04"}],"page":1,"pages":1}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"errors":[{"reason":"not found"}]}`)
		}
	}))
}

// TestAccountTokensHTTP covers the current user's PAT (account) endpoints.
func TestAccountTokensHTTP(t *testing.T) {
	h := newRebuildHarness(t)
	srv, session := rebuildServer(t, h)

	// 初始：rebuildServer 已为 admin 绑定 test-token。
	req := httptest.NewRequest(http.MethodGet, "/api/account/tokens", nil)
	authed(req, session)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	wantStatus(t, rr, http.StatusOK)
	var list struct {
		Tokens []tokenJSON `json:"tokens"`
	}
	decodeJSON(t, rr, &list)
	if len(list.Tokens) != 1 {
		t.Fatalf("initial tokens = %+v", list.Tokens)
	}

	// 添加。
	req2 := httptest.NewRequest(http.MethodPost, "/api/account/tokens", bytes.NewBufferString(`{"label":"prod","token":"linode_pat_x"}`))
	authed(req2, session)
	rr2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr2, req2)
	wantStatus(t, rr2, http.StatusOK)

	// 列表含新加的 prod 且已掩码。
	req3 := httptest.NewRequest(http.MethodGet, "/api/account/tokens", nil)
	authed(req3, session)
	rr3 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr3, req3)
	decodeJSON(t, rr3, &list)
	if len(list.Tokens) != 2 {
		t.Fatalf("after add = %+v", list.Tokens)
	}
	var prod *tokenJSON
	for i := range list.Tokens {
		if list.Tokens[i].Label == "prod" {
			prod = &list.Tokens[i]
		}
	}
	if prod == nil || prod.Masked == "linode_pat_x" {
		t.Fatalf("prod not found or not masked: %+v", list.Tokens)
	}

	// 删除 prod。
	req4 := httptest.NewRequest(http.MethodDelete, "/api/account/tokens/"+strconv.Itoa(prod.ID), nil)
	authed(req4, session)
	rr4 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr4, req4)
	wantStatus(t, rr4, http.StatusOK)

	req5 := httptest.NewRequest(http.MethodGet, "/api/account/tokens", nil)
	authed(req5, session)
	rr5 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr5, req5)
	decodeJSON(t, rr5, &list)
	if len(list.Tokens) != 1 {
		t.Fatalf("after delete = %+v", list.Tokens)
	}
}

func TestAccountTokensWithHealth(t *testing.T) {
	mock := accountHealthAPI(t)
	defer mock.Close()
	h := newRebuildHarness(t)
	srv, session := rebuildServer(t, h)
	srv.APIBase = mock.URL

	// 为 admin 绑定 4 个不同状态的 PAT。
	for _, p := range []struct {
		label string
		pat   string
	}{
		{"ok", "pat_ok"},
		{"no_balance", "pat_nobalance"},
		{"banned", "pat_banned"},
		{"error", "pat_unknown"},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/account/tokens",
			bytes.NewBufferString(`{"label":"`+p.label+`","token":"`+p.pat+`"}`))
		authed(req, session)
		srv.Handler().ServeHTTP(httptest.NewRecorder(), req)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/account/tokens", nil)
	authed(req, session)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	wantStatus(t, rr, http.StatusOK)

	var list struct {
		Tokens []tokenJSON `json:"tokens"`
	}
	decodeJSON(t, rr, &list)
	if len(list.Tokens) != 5 {
		t.Fatalf("tokens = %d, want 5 (body=%s)", len(list.Tokens), rr.Body.String())
	}

	wantStatusByLabel := map[string]string{
		"ok":         HealthOK,
		"no_balance": HealthNoBalance,
		"banned":     HealthBanned,
		"error":      HealthError,
	}
	for _, tok := range list.Tokens {
		want, found := wantStatusByLabel[tok.Label]
		if !found {
			// test-token 没有 mock 的 /account 路由 → 404 → error
			if tok.Label != "test-token" {
				t.Fatalf("unexpected label %q", tok.Label)
			}
			if tok.HealthStatus != HealthError {
				t.Fatalf("test-token: health_status = %q, want %q", tok.HealthStatus, HealthError)
			}
			continue
		}
		if tok.HealthStatus != want {
			t.Fatalf("label=%q: health_status = %q, want %q (detail=%q)", tok.Label, tok.HealthStatus, want, tok.HealthDetail)
		}
		if tok.HealthCheckedAt == "" {
			t.Fatalf("label=%q: health_checked_at empty", tok.Label)
		}
		if want == HealthOK && (tok.Balance != -1.5 || tok.BalanceUninvoiced != 0) {
			t.Fatalf("label=ok: balance=%.2f uninvoiced=%.2f, want -1.5/0", tok.Balance, tok.BalanceUninvoiced)
		}
		if want == HealthNoBalance && tok.Balance != 2.0 {
			t.Fatalf("label=no_balance: balance = %.2f, want 2.0", tok.Balance)
		}
	}
}

// TestAccountOptionsAndCreateHTTP covers create-instance option loading and
// instance creation against a mocked Linode API.
func TestAccountOptionsAndCreateHTTP(t *testing.T) {
	mock := mockLinodeAPI(t)
	defer mock.Close()
	h := newRebuildHarness(t)
	srv, session := rebuildServer(t, h)
	srv.APIBase = mock.URL

	// 绑定一个 PAT。
	req := httptest.NewRequest(http.MethodPost, "/api/account/tokens", bytes.NewBufferString(`{"label":"main","token":"linode_pat_1"}`))
	authed(req, session)
	srv.Handler().ServeHTTP(httptest.NewRecorder(), req)

	// 加载 region/type/image。
	req2 := httptest.NewRequest(http.MethodGet, "/api/account/options?token_id=1", nil)
	authed(req2, session)
	rr2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr2, req2)
	wantStatus(t, rr2, http.StatusOK)
	var opts struct {
		Regions []linode.Region     `json:"regions"`
		Types   []linode.LinodeType `json:"types"`
		Images  []linode.Image      `json:"images"`
	}
	decodeJSON(t, rr2, &opts)
	if len(opts.Regions) != 4 || len(opts.Types) != 3 || len(opts.Images) != 3 {
		t.Fatalf("opts = %+v", opts)
	}
	// 三个列表均按固定顺序排序：regions 按 country 升序（同国按 id），types/images 按 id 升序。
	wantRegionIDs := []string{"de-fra-2", "eu-central", "us-east", "us-west"}
	for i, want := range wantRegionIDs {
		if opts.Regions[i].ID != want {
			t.Fatalf("regions[%d] = %q, want %q (sorted)", i, opts.Regions[i].ID, want)
		}
	}
	wantTypeIDs := []string{"g6-standard-1", "g6-standard-2", "g6-standard-4"}
	for i, want := range wantTypeIDs {
		if opts.Types[i].ID != want {
			t.Fatalf("types[%d] = %q, want %q (sorted)", i, opts.Types[i].ID, want)
		}
	}
	wantImageIDs := []string{"linode/debian12", "linode/ubuntu22.04", "linode/ubuntu24.04"}
	for i, want := range wantImageIDs {
		if opts.Images[i].ID != want {
			t.Fatalf("images[%d] = %q, want %q (sorted)", i, opts.Images[i].ID, want)
		}
	}

	// 创建实例（带可选开机脚本）。
	req3 := httptest.NewRequest(http.MethodPost, "/api/account/instances",
		bytes.NewBufferString(`{"token_id":1,"label":"myinst","region":"us-east","type":"g6-standard-1","image":"linode/debian12","root_pass":"Secret123!","script":"#!/usr/bin/env bash\necho hi\n"}`))
	authed(req3, session)
	rr3 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr3, req3)
	wantStatus(t, rr3, http.StatusCreated)

	// 开机脚本已保存为该实例配置（mock 返回的实例 id 为 100）。
	if got, ok := srv.Store.InstanceScript(100); !ok || got != "#!/usr/bin/env bash\necho hi" {
		t.Fatalf("InstanceScript(100) = %q, %v; want saved script", got, ok)
	}
	// root 密码也保存为该实例配置，供重建时继承。
	if got, ok := srv.Store.InstanceRootPassword(100); !ok || got != "Secret123!" {
		t.Fatalf("InstanceRootPassword(100) = %q, %v; want saved password", got, ok)
	}

	// 不填脚本（或全空白）时不清除已有配置。
	req4 := httptest.NewRequest(http.MethodPost, "/api/account/instances",
		bytes.NewBufferString(`{"token_id":1,"label":"myinst2","region":"us-east","type":"g6-standard-1","image":"linode/debian12","root_pass":"Secret123!","script":"   "}`))
	authed(req4, session)
	rr4 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr4, req4)
	wantStatus(t, rr4, http.StatusCreated)
	if got, ok := srv.Store.InstanceScript(100); !ok || got != "#!/usr/bin/env bash\necho hi" {
		t.Fatalf("after blank script, InstanceScript(100) = %q, %v; want unchanged", got, ok)
	}

	// 首次请求已把 options 快照写入缓存，可直接读出。
	if snap := srv.Store.CachedAccountOptions(1); snap == nil {
		t.Fatalf("CachedAccountOptions(1) = nil; want cached snapshot")
	}

	// 关闭 mock 后再次读取：命中数据库缓存，不再请求 Linode API。
	mock.Close()
	req5 := httptest.NewRequest(http.MethodGet, "/api/account/options?token_id=1", nil)
	authed(req5, session)
	rr5 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr5, req5)
	wantStatus(t, rr5, http.StatusOK)
	var opts5 struct {
		Regions []linode.Region     `json:"regions"`
		Types   []linode.LinodeType `json:"types"`
		Images  []linode.Image      `json:"images"`
	}
	decodeJSON(t, rr5, &opts5)
	if len(opts5.Regions) != 4 || len(opts5.Types) != 3 || len(opts5.Images) != 3 {
		t.Fatalf("cached opts = regions=%d types=%d images=%d; want 4/3/3", len(opts5.Regions), len(opts5.Types), len(opts5.Images))
	}
	// 命中缓存时同样按 country 升序（同国按 id）返回，与实时拉取路径一致。
	for i, want := range wantRegionIDs {
		if opts5.Regions[i].ID != want {
			t.Fatalf("cached regions[%d] = %q, want %q (sorted)", i, opts5.Regions[i].ID, want)
		}
	}

	// ?refresh=1 绕过缓存（mock 已关闭，必然失败）。
	req6 := httptest.NewRequest(http.MethodGet, "/api/account/options?token_id=1&refresh=1", nil)
	authed(req6, session)
	rr6 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr6, req6)
	wantStatus(t, rr6, http.StatusBadGateway)
}
