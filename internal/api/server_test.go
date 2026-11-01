package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"linode-panel/internal/linode"
)

// fakeAPI records calls and delegates to per-method functions when set.
// It satisfies LinodeAPI and is used by the rebuild Manager tests and as the
// API seam for routes that do not go through a per-PAT client.
type fakeAPI struct {
	mu    sync.Mutex
	calls []string

	listInstances  func(context.Context) ([]linode.Instance, error)
	getInstance    func(context.Context, int) (linode.Instance, error)
	boot           func(context.Context, int) error
	shutdown       func(context.Context, int) error
	reboot         func(context.Context, int) error
	getIPs         func(context.Context, int) (linode.InstanceIPs, error)
	createInstance func(context.Context, linode.CreateOptions) (linode.Instance, error)
	deleteInstance func(context.Context, int) error
	attachFirewall func(context.Context, string, int) error
}

func (f *fakeAPI) record(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
}

func (f *fakeAPI) called(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == name {
			return true
		}
	}
	return false
}

func (f *fakeAPI) ListInstances(ctx context.Context) ([]linode.Instance, error) {
	f.record("ListInstances")
	if f.listInstances != nil {
		return f.listInstances(ctx)
	}
	return nil, nil
}

func (f *fakeAPI) GetInstance(ctx context.Context, id int) (linode.Instance, error) {
	f.record("GetInstance")
	if f.getInstance != nil {
		return f.getInstance(ctx, id)
	}
	return linode.Instance{}, nil
}

func (f *fakeAPI) Boot(ctx context.Context, id int) error {
	f.record("Boot")
	if f.boot != nil {
		return f.boot(ctx, id)
	}
	return nil
}

func (f *fakeAPI) Shutdown(ctx context.Context, id int) error {
	f.record("Shutdown")
	if f.shutdown != nil {
		return f.shutdown(ctx, id)
	}
	return nil
}

func (f *fakeAPI) Reboot(ctx context.Context, id int) error {
	f.record("Reboot")
	if f.reboot != nil {
		return f.reboot(ctx, id)
	}
	return nil
}

func (f *fakeAPI) GetInstanceIPs(ctx context.Context, id int) (linode.InstanceIPs, error) {
	f.record("GetInstanceIPs")
	if f.getIPs != nil {
		return f.getIPs(ctx, id)
	}
	return linode.InstanceIPs{}, nil
}

func (f *fakeAPI) CreateInstance(ctx context.Context, opts linode.CreateOptions) (linode.Instance, error) {
	f.record("CreateInstance")
	if f.createInstance != nil {
		return f.createInstance(ctx, opts)
	}
	return linode.Instance{}, nil
}

func (f *fakeAPI) DeleteInstance(ctx context.Context, id int) error {
	f.record("DeleteInstance")
	if f.deleteInstance != nil {
		return f.deleteInstance(ctx, id)
	}
	return nil
}

func (f *fakeAPI) AttachAllowAllFirewall(ctx context.Context, region string, linodeID int) error {
	f.record("AttachAllowAllFirewall")
	if f.attachFirewall != nil {
		return f.attachFirewall(ctx, region, linodeID)
	}
	return nil
}

func decodeJSON(t *testing.T, rr *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rr.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %s: %v (body=%s)", rr.Result().Status, err, rr.Body.String())
	}
}

func wantStatus(t *testing.T, rr *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rr.Code != want {
		t.Fatalf("status = %d, want %d (body=%s)", rr.Code, want, rr.Body.String())
	}
}

// linodeServerFor returns a fake Linode server backed by byPAT and a Server
// whose APIBase points at it, with alice logged in and given the PATs.
func linodeServerFor(t *testing.T, byPAT map[string][]linode.Instance) (*Server, *http.Cookie) {
	t.Helper()
	ts := fakeLinodeServer(t, byPAT)
	srv := testServer(t, &fakeAPI{})
	srv.APIBase = ts.URL
	cookie := loginAlice(t, srv)
	return srv, cookie
}

func TestListInstancesShape(t *testing.T) {
	srv, cookie := linodeServerFor(t, map[string][]linode.Instance{
		"pat1": {
			{
				ID: 1, Label: "web", Status: "running", Region: "us-east", Type: "g6-standard-1",
				IPv4: []string{"1.2.3.4"}, IPv6: "2600::1", Tags: []string{"prod"}, Created: "2024-01-01T00:00:00",
			},
			{
				ID: 2, Label: "v6only", Status: "stopped", Region: "eu-west", Type: "g6-standard-2",
				IPv6: "2001:db8::2", Tags: nil, Created: "2024-02-02T00:00:00",
			},
		},
	})
	addAliceToken(t, srv, "main", "pat1")
	rr := authedGet(t, srv, cookie, "/api/instances")
	wantStatus(t, rr, http.StatusOK)
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q", ct)
	}
	var resp struct {
		Instances []struct {
			ID         int      `json:"id"`
			Label      string   `json:"label"`
			Status     string   `json:"status"`
			Region     string   `json:"region"`
			Type       string   `json:"type"`
			IPv4       []string `json:"ipv4"`
			IPv6       string   `json:"ipv6"`
			SSHCommand string   `json:"ssh_command"`
			Tags       []string `json:"tags"`
			Created    string   `json:"created"`
			TokenID    int      `json:"token_id"`
			TokenLabel string   `json:"token_label"`
		} `json:"instances"`
	}
	decodeJSON(t, rr, &resp)
	if len(resp.Instances) != 2 {
		t.Fatalf("got %d instances", len(resp.Instances))
	}
	first := resp.Instances[0]
	if first.ID != 1 || first.Label != "web" || first.Status != "running" ||
		first.Region != "us-east" || first.Type != "g6-standard-1" ||
		len(first.IPv4) != 1 || first.IPv4[0] != "1.2.3.4" || first.IPv6 != "2600::1" ||
		first.SSHCommand != "ssh root@1.2.3.4" || len(first.Tags) != 1 || first.Tags[0] != "prod" ||
		first.Created != "2024-01-01T00:00:00" || first.TokenID != 1 || first.TokenLabel != "main" {
		t.Fatalf("unexpected first instance: %+v", first)
	}
	second := resp.Instances[1]
	if second.SSHCommand != "ssh root@2001:db8::2" {
		t.Fatalf("second ssh_command = %q", second.SSHCommand)
	}
	if second.Tags == nil || len(second.Tags) != 0 {
		t.Fatalf("second tags = %#v, want empty slice", second.Tags)
	}
}

func TestListInstancesNoToken(t *testing.T) {
	srv, cookie := linodeServerFor(t, map[string][]linode.Instance{})
	rr := authedGet(t, srv, cookie, "/api/instances")
	wantStatus(t, rr, http.StatusOK)
	var resp struct {
		Instances []struct{} `json:"instances"`
	}
	decodeJSON(t, rr, &resp)
	if resp.Instances == nil {
		t.Fatalf("instances = nil, want empty array")
	}
}

func TestListInstancesSSHPort(t *testing.T) {
	srv, cookie := linodeServerFor(t, map[string][]linode.Instance{
		"pat1": {{ID: 1, Label: "web", Status: "running", IPv4: []string{"10.0.0.1"}}},
	})
	addAliceToken(t, srv, "main", "pat1")
	srv.SSHPort = 2222
	rr := authedGet(t, srv, cookie, "/api/instances")
	wantStatus(t, rr, http.StatusOK)
	var resp struct {
		Instances []struct {
			SSHCommand string `json:"ssh_command"`
		} `json:"instances"`
	}
	decodeJSON(t, rr, &resp)
	if len(resp.Instances) != 1 || resp.Instances[0].SSHCommand != "ssh -p 2222 root@10.0.0.1" {
		t.Fatalf("ssh_command = %q", resp.Instances[0].SSHCommand)
	}
}

func TestListInstancesNoHost(t *testing.T) {
	srv, cookie := linodeServerFor(t, map[string][]linode.Instance{
		"pat1": {{ID: 1, Label: "web", Status: "running"}},
	})
	addAliceToken(t, srv, "main", "pat1")
	rr := authedGet(t, srv, cookie, "/api/instances")
	wantStatus(t, rr, http.StatusOK)
	var resp struct {
		Instances []struct {
			SSHCommand string `json:"ssh_command"`
		} `json:"instances"`
	}
	decodeJSON(t, rr, &resp)
	if resp.Instances[0].SSHCommand != "" {
		t.Fatalf("ssh_command = %q, want empty", resp.Instances[0].SSHCommand)
	}
}

func TestActionPassthrough(t *testing.T) {
	for _, tc := range []struct {
		path string
	}{
		{"/api/instances/7/boot"},
		{"/api/instances/7/shutdown"},
		{"/api/instances/7/reboot"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			srv, cookie := linodeServerFor(t, map[string][]linode.Instance{
				"pat1": {{ID: 7, Label: "web", Status: "running", IPv4: []string{"10.0.0.1"}}},
			})
			addAliceToken(t, srv, "main", "pat1")
			rr := authedPost(t, srv, cookie, tc.path, "")
			wantStatus(t, rr, http.StatusOK)
			var resp struct {
				OK bool `json:"ok"`
			}
			decodeJSON(t, rr, &resp)
			if !resp.OK {
				t.Fatalf("%s: ok = false", tc.path)
			}
		})
	}
}

func TestDeleteInstance(t *testing.T) {
	srv, cookie := linodeServerFor(t, map[string][]linode.Instance{
		"pat1": {{ID: 7, Label: "web", Status: "running", IPv4: []string{"10.0.0.1"}}},
	})
	addAliceToken(t, srv, "main", "pat1")
	tokID := srv.Store.TokensOf(aliceID)[0].ID
	path := "/api/instances/7?token_id=" + strconv.Itoa(tokID)

	// 先列出一次以填充该账号的缓存快照。
	wantStatus(t, authedGet(t, srv, cookie, "/api/instances"), http.StatusOK)
	if cached := srv.Store.CachedInstances(tokID); len(cached) != 1 {
		t.Fatalf("cached before delete = %d, want 1", len(cached))
	}

	rr := authedDelete(t, srv, cookie, path)
	wantStatus(t, rr, http.StatusOK)
	var resp struct {
		OK bool `json:"ok"`
	}
	decodeJSON(t, rr, &resp)
	if !resp.OK {
		t.Fatalf("ok = false")
	}
	// 删除后应清空该实例的缓存，列表不再展示。
	if cached := srv.Store.CachedInstances(tokID); len(cached) != 0 {
		t.Fatalf("cached after delete = %d, want 0", len(cached))
	}
}

func TestDeleteInstanceNotFound(t *testing.T) {
	srv, cookie := linodeServerFor(t, map[string][]linode.Instance{"pat1": {}})
	addAliceToken(t, srv, "main", "pat1")
	tokID := srv.Store.TokensOf(aliceID)[0].ID
	rr := authedDelete(t, srv, cookie, "/api/instances/99?token_id="+strconv.Itoa(tokID))
	wantStatus(t, rr, http.StatusNotFound)
}

func TestDeleteInstanceInvalidID(t *testing.T) {
	srv, cookie := linodeServerFor(t, map[string][]linode.Instance{"pat1": {}})
	addAliceToken(t, srv, "main", "pat1")
	rr := authedDelete(t, srv, cookie, "/api/instances/abc")
	wantStatus(t, rr, http.StatusBadRequest)
}

func TestStatusReady(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	srv, cookie := linodeServerFor(t, map[string][]linode.Instance{
		"pat1": {{ID: 3, Label: "web", Status: "running", IPv4: []string{"127.0.0.1"}}},
	})
	addAliceToken(t, srv, "main", "pat1")
	srv.SSHPort = port
	rr := authedGet(t, srv, cookie, "/api/instances/3/status")
	wantStatus(t, rr, http.StatusOK)
	var resp struct {
		ID         int    `json:"id"`
		Label      string `json:"label"`
		Status     string `json:"status"`
		SSHCommand string `json:"ssh_command"`
		SSHReady   bool   `json:"ssh_ready"`
		SSHError   string `json:"ssh_error"`
	}
	decodeJSON(t, rr, &resp)
	if resp.ID != 3 || resp.Label != "web" || resp.Status != "running" ||
		resp.SSHCommand != "ssh -p "+strconv.Itoa(port)+" root@127.0.0.1" ||
		!resp.SSHReady || resp.SSHError != "" {
		t.Fatalf("unexpected status: %+v", resp)
	}
}

func TestStatusClosedPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	srv, cookie := linodeServerFor(t, map[string][]linode.Instance{
		"pat1": {{ID: 4, Label: "web", Status: "running", IPv4: []string{"127.0.0.1"}}},
	})
	addAliceToken(t, srv, "main", "pat1")
	srv.SSHPort = port
	rr := authedGet(t, srv, cookie, "/api/instances/4/status")
	wantStatus(t, rr, http.StatusOK)
	var resp struct {
		SSHReady bool   `json:"ssh_ready"`
		SSHError string `json:"ssh_error"`
	}
	decodeJSON(t, rr, &resp)
	if resp.SSHReady {
		t.Fatal("ssh_ready = true, want false")
	}
	if resp.SSHError == "" {
		t.Fatal("ssh_error empty, want non-empty")
	}
}

func TestStatusNotRunningSkipsProbe(t *testing.T) {
	srv, cookie := linodeServerFor(t, map[string][]linode.Instance{
		"pat1": {{ID: 5, Label: "web", Status: "stopped", IPv4: []string{"203.0.113.9"}}},
	})
	addAliceToken(t, srv, "main", "pat1")
	rr := authedGet(t, srv, cookie, "/api/instances/5/status")
	wantStatus(t, rr, http.StatusOK)
	var resp struct {
		SSHCommand string `json:"ssh_command"`
		SSHReady   bool   `json:"ssh_ready"`
		SSHError   string `json:"ssh_error"`
	}
	decodeJSON(t, rr, &resp)
	if resp.SSHReady || resp.SSHError != "" {
		t.Fatalf("non-running should not probe: %+v", resp)
	}
	if resp.SSHCommand != "ssh root@203.0.113.9" {
		t.Fatalf("ssh_command = %q", resp.SSHCommand)
	}
}

func TestStatusIPv6Fallback(t *testing.T) {
	srv, cookie := linodeServerFor(t, map[string][]linode.Instance{
		"pat1": {{ID: 6, Label: "v6", Status: "running", IPv6: "2001:db8::6"}},
	})
	addAliceToken(t, srv, "main", "pat1")
	rr := authedGet(t, srv, cookie, "/api/instances/6/status")
	wantStatus(t, rr, http.StatusOK)
	var resp struct {
		SSHCommand string `json:"ssh_command"`
		SSHReady   bool   `json:"ssh_ready"`
	}
	decodeJSON(t, rr, &resp)
	if resp.SSHCommand != "ssh root@2001:db8::6" {
		t.Fatalf("ssh_command = %q", resp.SSHCommand)
	}
	if resp.SSHReady {
		t.Fatal("ssh_ready = true; IPv6 SLAAC not routable here, should probe fail (or skip)")
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		name     string
		wantCode int
		wantMsg  string
	}{
		{"unauthorized", http.StatusUnauthorized, "Linode Token 无效或权限不足"},
		{"forbidden", http.StatusUnauthorized, "Linode Token 无效或权限不足"},
		{"notfound", http.StatusNotFound, "实例不存在"},
		{"other-api", http.StatusBadGateway, "Linode API 错误: boom"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code := map[string]int{
				"unauthorized": 401,
				"forbidden":    403,
				"notfound":     404,
				"other-api":    500,
			}[tc.name]
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
				_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"reason": "boom"}}})
			}))
			t.Cleanup(ts.Close)
			srv := testServer(t, &fakeAPI{})
			srv.APIBase = ts.URL
			addAliceToken(t, srv, "main", "pat1")
			cookie := loginAlice(t, srv)
			// The status route propagates the Linode error through writeError.
			rr := authedGet(t, srv, cookie, "/api/instances/1/status")
			wantStatus(t, rr, tc.wantCode)
			var resp struct {
				Error string `json:"error"`
			}
			decodeJSON(t, rr, &resp)
			if resp.Error != tc.wantMsg {
				t.Fatalf("error = %q, want %q", resp.Error, tc.wantMsg)
			}
		})
	}
}

func TestInvalidID(t *testing.T) {
	srv, cookie := linodeServerFor(t, map[string][]linode.Instance{
		"pat1": {{ID: 1, Label: "web", Status: "running", IPv4: []string{"10.0.0.1"}}},
	})
	addAliceToken(t, srv, "main", "pat1")
	for _, path := range []string{"/api/instances/abc/boot", "/api/instances/abc/shutdown", "/api/instances/-3/boot"} {
		rr := authedPost(t, srv, cookie, path, "")
		wantStatus(t, rr, http.StatusBadRequest)
		var resp struct {
			Error string `json:"error"`
		}
		decodeJSON(t, rr, &resp)
		if resp.Error == "" {
			t.Fatalf("%s: empty error", path)
		}
	}
	rr := authedGet(t, srv, cookie, "/api/instances/abc/status")
	wantStatus(t, rr, http.StatusBadRequest)
}

func TestMethodNotAllowed(t *testing.T) {
	srv, cookie := linodeServerFor(t, map[string][]linode.Instance{
		"pat1": {{ID: 1, Label: "web", Status: "running", IPv4: []string{"10.0.0.1"}}},
	})
	addAliceToken(t, srv, "main", "pat1")
	req := httptest.NewRequest(http.MethodGet, "/api/instances/1/boot", nil)
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	wantStatus(t, rr, http.StatusMethodNotAllowed)
}

func TestSSHPortDefaultIs22(t *testing.T) {
	srv := testServer(t, &fakeAPI{})
	if got := srv.sshPort(); got != 22 {
		t.Fatalf("sshPort() = %d, want 22", got)
	}
	srv.SSHPort = 2222
	if got := srv.sshPort(); got != 2222 {
		t.Fatalf("sshPort() = %d, want 2222", got)
	}
}

func TestMultiPATAggregation(t *testing.T) {
	srv, cookie := linodeServerFor(t, map[string][]linode.Instance{
		"pat1": {
			{ID: 1, Label: "web", Status: "running", IPv4: []string{"10.0.0.1"}},
			{ID: 2, Label: "db", Status: "stopped", IPv4: []string{"10.0.0.2"}},
		},
		"pat2": {
			{ID: 3, Label: "cache", Status: "running", IPv4: []string{"10.0.0.3"}},
		},
	})
	addAliceToken(t, srv, "main", "pat1")
	addAliceToken(t, srv, "backup", "pat2")
	rr := authedGet(t, srv, cookie, "/api/instances")
	wantStatus(t, rr, http.StatusOK)
	var resp struct {
		Instances []struct {
			ID         int    `json:"id"`
			Label      string `json:"label"`
			TokenID    int    `json:"token_id"`
			TokenLabel string `json:"token_label"`
		} `json:"instances"`
	}
	decodeJSON(t, rr, &resp)
	if len(resp.Instances) != 3 {
		t.Fatalf("instances = %d, want 3 (body=%s)", len(resp.Instances), rr.Body.String())
	}
	byID := map[int]struct {
		TokenID    int
		TokenLabel string
	}{}
	for _, in := range resp.Instances {
		byID[in.ID] = struct {
			TokenID    int
			TokenLabel string
		}{in.TokenID, in.TokenLabel}
	}
	if byID[1].TokenLabel != "main" || byID[3].TokenLabel != "backup" {
		t.Fatalf("token labels = %+v", byID)
	}
	if byID[1].TokenID == byID[3].TokenID {
		t.Fatalf("token ids should differ: %+v", byID)
	}
}

func TestPATLevelFailureSkipped(t *testing.T) {
	// pat2 is never served by the fake server, so its PAT request fails with
	// 404; the list must still return pat1's instances.
	srv, cookie := linodeServerFor(t, map[string][]linode.Instance{
		"pat1": {{ID: 1, Label: "web", Status: "running", IPv4: []string{"10.0.0.1"}}},
	})
	addAliceToken(t, srv, "main", "pat1")
	addAliceToken(t, srv, "broken", "missing-pat")
	rr := authedGet(t, srv, cookie, "/api/instances")
	wantStatus(t, rr, http.StatusOK)
	var resp struct {
		Instances []struct {
			ID int `json:"id"`
		} `json:"instances"`
	}
	decodeJSON(t, rr, &resp)
	if len(resp.Instances) != 1 || resp.Instances[0].ID != 1 {
		t.Fatalf("instances = %+v, want only pat1's instance (body=%s)", resp.Instances, rr.Body.String())
	}
}

func TestResolveTokenDefaultAndInvalid(t *testing.T) {
	srv, cookie := linodeServerFor(t, map[string][]linode.Instance{
		"pat1": {{ID: 1, Label: "web", Status: "running", IPv4: []string{"10.0.0.1"}}},
	})
	addAliceToken(t, srv, "main", "pat1")
	// No token_id -> uses first PAT -> 200.
	rr := authedGet(t, srv, cookie, "/api/instances/1/status")
	wantStatus(t, rr, http.StatusOK)
	// Explicit first token id (1) -> 200.
	rr = authedGet(t, srv, cookie, "/api/instances/1/status?token_id=1")
	wantStatus(t, rr, http.StatusOK)
	// Invalid token id -> 400.
	rr = authedGet(t, srv, cookie, "/api/instances/1/status?token_id=999")
	wantStatus(t, rr, http.StatusBadRequest)
	var resp struct {
		Error string `json:"error"`
	}
	decodeJSON(t, rr, &resp)
	if resp.Error != "PAT 不存在" {
		t.Fatalf("error = %q, want PAT 不存在", resp.Error)
	}
	// Non-numeric token id -> 400.
	rr = authedGet(t, srv, cookie, "/api/instances/1/status?token_id=abc")
	wantStatus(t, rr, http.StatusBadRequest)
}

func TestNoPATListEmpty(t *testing.T) {
	srv, cookie := linodeServerFor(t, map[string][]linode.Instance{})
	rr := authedGet(t, srv, cookie, "/api/instances")
	wantStatus(t, rr, http.StatusOK)
	var resp struct {
		Instances []struct{} `json:"instances"`
	}
	decodeJSON(t, rr, &resp)
	if resp.Instances == nil || len(resp.Instances) != 0 {
		t.Fatalf("instances = %#v, want empty non-nil array", resp.Instances)
	}
}
