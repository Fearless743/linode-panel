package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"linode-panel/internal/linode"
	"linode-panel/internal/sshrun"
	"linode-panel/internal/store"
)

// rebuildHarness wires a fakeAPI plus a Manager with injectable RunFn and a
// fast poll loop so tests complete quickly.
type rebuildHarness struct {
	api *fakeAPI
	mgr *Manager

	mu      sync.Mutex
	status  map[int]string // instance id → status, for stateful fakes
	runPATs []string       // PATs passed to Start, in order
}

func newRebuildHarness(t *testing.T) *rebuildHarness {
	t.Helper()
	h := &rebuildHarness{api: &fakeAPI{}, status: map[int]string{}}
	h.mgr = NewManager(h.api)
	h.mgr.PollInterval = 1 * time.Millisecond
	h.mgr.PollTimeout = 100 * time.Millisecond
	h.mgr.ProbeFn = func(_ context.Context, _ string) error { return nil }
	// Every rebuild uses a per-PAT client; route it to the fake so the run
	// still exercises the recorded calls, and record the PAT.
	h.mgr.newAPI = func(pat string) LinodeAPI {
		h.mu.Lock()
		h.runPATs = append(h.runPATs, pat)
		h.mu.Unlock()
		return h.api
	}
	return h
}

// newStatefulHarness wires status-transitioning fakes: Shutdown sets the
// instance offline, Boot sets it running, and CreateInstance registers the
// new id as running.
func newStatefulHarness(t *testing.T) *rebuildHarness {
	t.Helper()
	h := newRebuildHarness(t)
	base := linode.Instance{Region: "us-east", Type: "g6-standard-1", Image: "linode/debian12", IPv4: []string{"198.51.100.5"}}
	h.api.getInstance = func(_ context.Context, id int) (linode.Instance, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		st := h.status[id]
		if st == "" {
			st = "running"
		}
		inst := base
		inst.ID = id
		inst.Status = st
		return inst, nil
	}
	h.api.shutdown = func(_ context.Context, id int) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.status[id] = "offline"
		return nil
	}
	h.api.boot = func(_ context.Context, id int) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.status[id] = "running"
		return nil
	}
	h.api.createInstance = func(_ context.Context, opts linode.CreateOptions) (linode.Instance, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		id := 99
		h.status[id] = "running"
		inst := base
		inst.ID = id
		inst.Status = "running"
		return inst, nil
	}
	h.api.getIPs = func(_ context.Context, id int) (linode.InstanceIPs, error) {
		var ips linode.InstanceIPs
		ips.IPv4.Public = []linode.IPAddress{{Address: "203.0.113.7", Public: true}}
		return ips, nil
	}
	return h
}

// sequence returns the recorded fakeAPI call names.
func (h *rebuildHarness) sequence() []string {
	h.api.mu.Lock()
	defer h.api.mu.Unlock()
	return append([]string(nil), h.api.calls...)
}

// runToCompletion waits until the task leaves "running".
func (m *Manager) runToCompletion(t *testing.T, id string) Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		task, err := m.Get(id)
		if err != nil {
			t.Fatalf("Get(%s): %v", id, err)
		}
		if task.Status != StatusRunning {
			return task
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("task %s did not finish in time", id)
	return Task{}
}

// startRebuild starts a rebuild against the fake API and returns the task id.
func startRebuild(t *testing.T, mgr *Manager, id int, script, reserved string) string {
	t.Helper()
	taskID, err := mgr.Start(id, script, reserved, "", "")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return taskID
}

// TestRebuildHappyPath drives the full success sequence with a reserved IP.
func TestRebuildHappyPath(t *testing.T) {
	h := newStatefulHarness(t)
	// Override createInstance to also assert the exact create options.
	h.api.createInstance = func(_ context.Context, opts linode.CreateOptions) (linode.Instance, error) {
		if opts.Region != "us-east" || opts.Type != "g6-standard-1" || opts.Image != "linode/debian12" {
			t.Fatalf("CreateInstance got wrong opts: %+v", opts)
		}
		if opts.Booted == nil || !*opts.Booted {
			t.Fatalf("CreateInstance Booted not true: %+v", opts)
		}
		if len(opts.AuthorizedKeys) != 1 || !strings.HasPrefix(opts.AuthorizedKeys[0], "ssh-ed25519 ") {
			t.Fatalf("CreateInstance AuthorizedKeys not generated: %+v", opts)
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		id := 99
		h.status[id] = "running"
		inst := linode.Instance{ID: id, Region: opts.Region, Type: opts.Type, Image: opts.Image, Status: "running"}
		return inst, nil
	}
	ran := false
	h.mgr.RunFn = func(_ context.Context, opts sshrun.Options, script string) (sshrun.Result, error) {
		ran = true
		if opts.Host != "203.0.113.7" || opts.Port != 22 || opts.User != "" {
			t.Fatalf("RunFn got wrong opts: %+v", opts)
		}
		if len(opts.PrivateKey) == 0 || !opts.InsecureSkipVerify {
			t.Fatalf("RunFn opts missing key/skip-verify: %+v", opts)
		}
		if script != "echo hi" {
			t.Fatalf("RunFn got wrong script: %q", script)
		}
		return sshrun.Result{Exit: 0, Stdout: "ok\n"}, nil
	}
	h.mgr.sshPort = 22

	id := startRebuild(t, h.mgr, 42, "echo hi", "192.0.2.10")
	task := h.mgr.runToCompletion(t, id)
	if task.Status != StatusSuccess {
		t.Fatalf("status = %s, want success (err=%q log=%v)", task.Status, task.Err, task.Log)
	}
	if task.NewIP != "203.0.113.7" {
		t.Fatalf("NewIP = %q", task.NewIP)
	}
	if task.OldIP != "198.51.100.5" {
		t.Fatalf("OldIP = %q", task.OldIP)
	}
	if !ran {
		t.Fatalf("RunFn never called")
	}

	got := h.sequence()
	for _, want := range []string{"GetInstance", "Shutdown", "GetConfigID", "UpdateConfigNetworkHelper", "DeleteInstance", "CreateInstance", "AttachAllowAllFirewall", "AssignIP", "Boot", "GetInstanceIPs"} {
		found := false
		for _, g := range got {
			if g == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("sequence missing %s (got %v)", want, got)
		}
	}
}

// TestRebuildCreateFails verifies a create failure marks the task failed.
func TestRebuildCreateFails(t *testing.T) {
	h := newRebuildHarness(t)
	h.api.getInstance = func(_ context.Context, id int) (linode.Instance, error) {
		return linode.Instance{ID: id, Region: "us-east", Type: "g6-standard-1", Image: "linode/debian12"}, nil
	}
	h.api.createInstance = func(_ context.Context, _ linode.CreateOptions) (linode.Instance, error) {
		return linode.Instance{}, errors.New("boom")
	}

	id := startRebuild(t, h.mgr, 42, "echo hi", "")
	task := h.mgr.runToCompletion(t, id)
	if task.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", task.Status)
	}
	if !strings.Contains(task.Err, "创建新实例失败") {
		t.Fatalf("Err = %q", task.Err)
	}
	if task.EndedAt == "" {
		t.Fatalf("EndedAt not set")
	}
}

// TestRebuildFirewallAttachFailsNonFatal verifies a failed allow-all firewall
// attach is logged but does not fail the rebuild.
func TestRebuildFirewallAttachFailsNonFatal(t *testing.T) {
	h := newStatefulHarness(t)
	// 绑定防火墙失败，但重建仍应成功。
	h.api.attachFirewall = func(_ context.Context, _ string, _ int) error {
		return errors.New("firewall scope missing")
	}
	h.mgr.RunFn = func(_ context.Context, _ sshrun.Options, _ string) (sshrun.Result, error) {
		return sshrun.Result{Exit: 0}, nil
	}

	id := startRebuild(t, h.mgr, 42, "echo hi", "")
	task := h.mgr.runToCompletion(t, id)
	if task.Status != StatusSuccess {
		t.Fatalf("status = %s, want success (err=%q log=%v)", task.Status, task.Err, task.Log)
	}
	found := false
	for _, line := range task.Log {
		if strings.Contains(line, "防火墙绑定失败") {
			found = true
		}
	}
	if !found {
		t.Fatalf("log does not mention firewall attach failure: %v", task.Log)
	}
}

// TestRebuildPollTimeout verifies a running-status poll timeout fails the task.
func TestRebuildPollTimeout(t *testing.T) {
	h := newRebuildHarness(t)
	h.api.getInstance = func(_ context.Context, id int) (linode.Instance, error) {
		if id == 42 {
			return linode.Instance{ID: 42, Region: "r", Type: "t", Image: "i", Status: "running"}, nil
		}
		// New instance never becomes running.
		return linode.Instance{ID: id, Region: "r", Type: "t", Image: "i", Status: "provisioning"}, nil
	}
	h.api.createInstance = func(_ context.Context, _ linode.CreateOptions) (linode.Instance, error) {
		return linode.Instance{ID: 99, Region: "r", Type: "t", Image: "i"}, nil
	}

	id := startRebuild(t, h.mgr, 42, "echo hi", "")
	task := h.mgr.runToCompletion(t, id)
	if task.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", task.Status)
	}
	if !strings.Contains(task.Err, "超时") {
		t.Fatalf("Err = %q", task.Err)
	}
}

// TestRebuildSSHNonZeroExit verifies a non-zero SSH exit fails but records
// stdout/stderr in the log.
func TestRebuildSSHNonZeroExit(t *testing.T) {
	h := newStatefulHarness(t)
	h.mgr.RunFn = func(_ context.Context, _ sshrun.Options, _ string) (sshrun.Result, error) {
		return sshrun.Result{Exit: 3, Stdout: "out", Stderr: "some error line"}, nil
	}

	id := startRebuild(t, h.mgr, 42, "bad script", "")
	task := h.mgr.runToCompletion(t, id)
	if task.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", task.Status)
	}
	if !strings.Contains(task.Err, "退出码 3") || !strings.Contains(task.Err, "some error line") {
		t.Fatalf("Err = %q, want exit code + stderr", task.Err)
	}
}

// TestRebuildSSHTransportError verifies a transport/auth failure fails the
// task with the error recorded.
func TestRebuildSSHTransportError(t *testing.T) {
	h := newStatefulHarness(t)
	h.mgr.RunFn = func(_ context.Context, _ sshrun.Options, _ string) (sshrun.Result, error) {
		return sshrun.Result{}, errors.New("dial tcp: connection refused")
	}

	id := startRebuild(t, h.mgr, 42, "echo hi", "")
	task := h.mgr.runToCompletion(t, id)
	if task.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", task.Status)
	}
	if !strings.Contains(task.Err, "SSH 执行开机脚本失败") || !strings.Contains(task.Err, "connection refused") {
		t.Fatalf("Err = %q", task.Err)
	}
}

// TestManagerConcurrency verifies the one-at-a-time limit returns ErrBusy.
func TestManagerConcurrency(t *testing.T) {
	h := newRebuildHarness(t)
	// Block the run inside the fake GetInstance so the first task stays running.
	release := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	h.api.getInstance = func(_ context.Context, id int) (linode.Instance, error) {
		once.Do(func() { close(started) })
		<-release
		return linode.Instance{ID: id, Region: "r", Type: "t", Image: "i"}, nil
	}

	id, err := h.mgr.Start(42, "echo hi", "", "", "")
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	<-started
	// second Start while busy
	if _, err := h.mgr.Start(43, "echo hi", "", "", ""); !errors.Is(err, ErrBusy) {
		t.Fatalf("second Start err = %v, want ErrBusy", err)
	}
	close(release)
	task := h.mgr.runToCompletion(t, id)
	if task.Status != StatusFailed {
		t.Fatalf("status = %s", task.Status)
	}
	// after completion a new Start must succeed
	if _, err := h.mgr.Start(44, "echo hi", "", "", ""); err != nil {
		t.Fatalf("Start after completion: %v", err)
	}
}

// TestTaskListNewestFirst verifies List orders newest first and reverses logs.
func TestTaskListNewestFirst(t *testing.T) {
	h := newRebuildHarness(t)
	h.api.getInstance = func(_ context.Context, id int) (linode.Instance, error) {
		return linode.Instance{ID: id, Region: "r", Type: "t", Image: "i"}, nil
	}
	id1 := startRebuild(t, h.mgr, 42, "s1", "")
	h.mgr.runToCompletion(t, id1)
	id2 := startRebuild(t, h.mgr, 43, "s2", "")
	h.mgr.runToCompletion(t, id2)

	list := h.mgr.List()
	if len(list) != 2 {
		t.Fatalf("List len = %d", len(list))
	}
	if list[0].ID != id2 || list[1].ID != id1 {
		t.Fatalf("List order = %s,%s want %s,%s", list[0].ID, list[1].ID, id2, id1)
	}
	// Logs reversed: newest log line first.
	if len(list[0].Log) == 0 {
		t.Fatalf("empty log")
	}
	// Task snapshot via Get also reverses.
	got, err := h.mgr.Get(id1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Log) == 0 || !strings.Contains(got.Log[0], "step") {
		t.Fatalf("Get log not reversed: %v", got.Log)
	}
}

// TestManagerGetNotFound verifies ErrNotFound.
func TestManagerGetNotFound(t *testing.T) {
	h := newRebuildHarness(t)
	if _, err := h.mgr.Get("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get err = %v, want ErrNotFound", err)
	}
}

// --- HTTP handler tests ---

// rebuildServer builds a Server wired to a harness with a working manager and
// a store whose admin user (group 1, all perms) has one PAT. It returns the
// server and a valid session cookie value for authenticated requests.
func rebuildServer(t *testing.T, h *rebuildHarness) (*Server, string) {
	t.Helper()
	st, _, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if _, err := st.AddToken(1, "test-token", "linode_pat_test"); err != nil {
		t.Fatalf("AddToken: %v", err)
	}
	session := st.NewSession(1, time.Hour)
	s := New(h.api, st)
	s.Tasks = h.mgr
	return s, session
}

// authed adds the session cookie to req for use with rebuildServer's session.
func authed(req *http.Request, session string) {
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
}

// TestRebuildHTTPEmptyScriptFallsBackToDefault: an empty script falls back to
// the built-in default template when the instance has no script configured.
func TestRebuildHTTPEmptyScriptFallsBackToDefault(t *testing.T) {
	h := newRebuildHarness(t)
	h.api.getInstance = func(_ context.Context, id int) (linode.Instance, error) {
		return linode.Instance{ID: id, Region: "r", Type: "t", Image: "i"}, nil
	}
	srv, session := rebuildServer(t, h)
	body := `{"script":"","reserved_ip":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/instances/42/rebuild", bytes.NewBufferString(body))
	authed(req, session)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	wantStatus(t, rr, http.StatusAccepted)
	var resp rebuildResponse
	decodeJSON(t, rr, &resp)
	task, err := h.mgr.Get(resp.TaskID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if task.Script != DefaultBootScript {
		t.Fatalf("script = %q, want default", task.Script)
	}
}

// TestRebuildHTTPUsesInstanceScript: an instance's configured script wins over
// the default when the request provides none.
func TestRebuildHTTPUsesInstanceScript(t *testing.T) {
	h := newRebuildHarness(t)
	h.api.getInstance = func(_ context.Context, id int) (linode.Instance, error) {
		return linode.Instance{ID: id, Region: "r", Type: "t", Image: "i"}, nil
	}
	srv, session := rebuildServer(t, h)
	// 预先配置实例脚本。
	req0 := httptest.NewRequest(http.MethodPut, "/api/instances/42/script", bytes.NewBufferString(`{"script":"echo per-instance"}`))
	authed(req0, session)
	srv.Handler().ServeHTTP(httptest.NewRecorder(), req0)

	req := httptest.NewRequest(http.MethodPost, "/api/instances/42/rebuild", bytes.NewBufferString(`{"script":""}`))
	authed(req, session)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	wantStatus(t, rr, http.StatusAccepted)
	var resp rebuildResponse
	decodeJSON(t, rr, &resp)
	task, err := h.mgr.Get(resp.TaskID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if task.Script != "echo per-instance" {
		t.Fatalf("script = %q, want per-instance", task.Script)
	}
}

// TestInstanceScriptHTTP covers the per-instance script endpoints.
func TestInstanceScriptHTTP(t *testing.T) {
	h := newRebuildHarness(t)
	srv, session := rebuildServer(t, h)

	// 初始：未配置 → 默认脚本，configured=false。
	req := httptest.NewRequest(http.MethodGet, "/api/instances/42/script", nil)
	authed(req, session)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	wantStatus(t, rr, http.StatusOK)
	var got struct {
		Script     string `json:"script"`
		Configured bool   `json:"configured"`
	}
	decodeJSON(t, rr, &got)
	if got.Configured || got.Script != DefaultBootScript {
		t.Fatalf("initial = %+v", got)
	}

	// PUT 配置。
	req2 := httptest.NewRequest(http.MethodPut, "/api/instances/42/script", bytes.NewBufferString(`{"script":"echo custom"}`))
	authed(req2, session)
	rr2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr2, req2)
	wantStatus(t, rr2, http.StatusOK)

	// GET → 自定义脚本。
	req3 := httptest.NewRequest(http.MethodGet, "/api/instances/42/script", nil)
	authed(req3, session)
	rr3 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr3, req3)
	decodeJSON(t, rr3, &got)
	if !got.Configured || got.Script != "echo custom" {
		t.Fatalf("after put = %+v", got)
	}

	// status 列表含 42。
	req4 := httptest.NewRequest(http.MethodGet, "/api/instance-scripts", nil)
	authed(req4, session)
	rr4 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr4, req4)
	var st struct {
		Configured []int `json:"configured"`
	}
	decodeJSON(t, rr4, &st)
	if len(st.Configured) != 1 || st.Configured[0] != 42 {
		t.Fatalf("status = %+v", st)
	}

	// DELETE → 恢复默认。
	req5 := httptest.NewRequest(http.MethodDelete, "/api/instances/42/script", nil)
	authed(req5, session)
	rr5 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr5, req5)
	wantStatus(t, rr5, http.StatusOK)

	req6 := httptest.NewRequest(http.MethodGet, "/api/instances/42/script", nil)
	authed(req6, session)
	rr6 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr6, req6)
	decodeJSON(t, rr6, &got)
	if got.Configured {
		t.Fatalf("still configured after delete: %+v", got)
	}
}

func TestRebuildHTTPConflict(t *testing.T) {
	h := newRebuildHarness(t)
	release := make(chan struct{})
	h.api.getInstance = func(_ context.Context, id int) (linode.Instance, error) {
		<-release
		return linode.Instance{ID: id, Region: "r", Type: "t", Image: "i"}, nil
	}
	srv, session := rebuildServer(t, h)
	// First request starts a task.
	body := `{"script":"echo hi"}`
	req := httptest.NewRequest(http.MethodPost, "/api/instances/42/rebuild", bytes.NewBufferString(body))
	authed(req, session)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	wantStatus(t, rr, http.StatusAccepted)
	var resp rebuildResponse
	decodeJSON(t, rr, &resp)
	if resp.TaskID == "" {
		t.Fatalf("empty task_id")
	}
	// Second request while busy.
	req2 := httptest.NewRequest(http.MethodPost, "/api/instances/43/rebuild", bytes.NewBufferString(body))
	authed(req2, session)
	rr2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr2, req2)
	wantStatus(t, rr2, http.StatusConflict)
	close(release)
}

func TestRebuildHTTPTaskRoutes(t *testing.T) {
	h := newRebuildHarness(t)
	h.api.getInstance = func(_ context.Context, id int) (linode.Instance, error) {
		return linode.Instance{ID: id, Region: "r", Type: "t", Image: "i"}, nil
	}
	srv, session := rebuildServer(t, h)
	body := `{"script":"echo hi"}`
	req := httptest.NewRequest(http.MethodPost, "/api/instances/42/rebuild", bytes.NewBufferString(body))
	authed(req, session)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	var resp rebuildResponse
	decodeJSON(t, rr, &resp)

	// Wait for completion.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		task, err := h.mgr.Get(resp.TaskID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if task.Status != StatusRunning {
			break
		}
		time.Sleep(time.Millisecond)
	}

	// GET single task.
	req2 := httptest.NewRequest(http.MethodGet, "/api/rebuild/tasks/"+resp.TaskID, nil)
	authed(req2, session)
	rr2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr2, req2)
	var task Task
	decodeJSON(t, rr2, &task)
	if task.ID != resp.TaskID || task.InstanceID != 42 {
		t.Fatalf("task mismatch: %+v", task)
	}

	// GET task list.
	req3 := httptest.NewRequest(http.MethodGet, "/api/rebuild/tasks", nil)
	authed(req3, session)
	rr3 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr3, req3)
	var list struct {
		Tasks []Task `json:"tasks"`
	}
	decodeJSON(t, rr3, &list)
	if len(list.Tasks) != 1 {
		t.Fatalf("list len = %d", len(list.Tasks))
	}

	// GET unknown task.
	req4 := httptest.NewRequest(http.MethodGet, "/api/rebuild/tasks/missing", nil)
	authed(req4, session)
	rr4 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr4, req4)
}

// TestRebuildProbeFail verifies a failed SSH port probe fails the task.
func TestRebuildProbeFail(t *testing.T) {
	h := newStatefulHarness(t)
	h.mgr.RunFn = func(_ context.Context, _ sshrun.Options, _ string) (sshrun.Result, error) {
		return sshrun.Result{Exit: 0}, nil
	}
	h.mgr.ProbeFn = func(_ context.Context, _ string) error {
		return errors.New("probe refused")
	}

	id := startRebuild(t, h.mgr, 42, "echo hi", "")
	task := h.mgr.runToCompletion(t, id)
	if task.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", task.Status)
	}
	if !strings.Contains(task.Err, "SSH 端口探测失败") {
		t.Fatalf("Err = %q", task.Err)
	}
}

func TestProbeTimeout(t *testing.T) {
	// 127.0.0.1 with an immediate-refusal closed port: probe must fail.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := probe(ctx, "127.0.0.1", 1)
	if err == nil {
		t.Fatalf("probe to closed port succeeded")
	}
}
