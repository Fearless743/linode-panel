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
			// 故意乱序返回，验证 handler 按 id 排序。
			_, _ = io.WriteString(w, `{"data":[{"id":"us-west","label":"Fremont"},{"id":"us-east","label":"Newark"},{"id":"eu-central","label":"Frankfurt"}],"page":1,"pages":1}`)
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
	if len(opts.Regions) != 3 || len(opts.Types) != 3 || len(opts.Images) != 3 {
		t.Fatalf("opts = %+v", opts)
	}
	// 三个列表均应按 id 字母升序排序。
	wantRegionIDs := []string{"eu-central", "us-east", "us-west"}
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
}
