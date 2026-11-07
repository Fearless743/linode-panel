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
func startRebuild(t *testing.T, mgr *Manager, id int, script string) string {
	t.Helper()
	taskID, err := mgr.Start(id, script, "Secret123!", "", "")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return taskID
}

// TestRebuildHappyPath drives the full success sequence.
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
		if opts.Password == "" || !opts.InsecureSkipVerify {
			t.Fatalf("RunFn opts missing password/skip-verify: %+v", opts)
		}
		if script != "echo hi" {
			t.Fatalf("RunFn got wrong script: %q", script)
		}
		return sshrun.Result{Exit: 0, Stdout: "ok\n"}, nil
	}
	h.mgr.sshPort = 22

	id := startRebuild(t, h.mgr, 42, "echo hi")
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
	for _, want := range []string{"GetInstance", "Shutdown", "DeleteInstance", "CreateInstance", "GetInstanceIPs"} {
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

// TestRebuildOldInstanceNotFoundCreatesNew verifies that a 404 while reading
// the old instance does not fail the rebuild: the replacement is created
// directly from the panel's cached spec, skipping shutdown/delete.
func TestRebuildOldInstanceNotFoundCreatesNew(t *testing.T) {
	h := newRebuildHarness(t)
	var createOpts linode.CreateOptions
	h.api.getInstance = func(_ context.Context, id int) (linode.Instance, error) {
		if id == 42 {
			return linode.Instance{}, &linode.APIError{StatusCode: http.StatusNotFound, Reason: "Not found"}
		}
		return linode.Instance{ID: id, Status: "running"}, nil
	}
	h.api.shutdown = func(context.Context, int) error {
		t.Fatal("Shutdown must be skipped when the old instance is already gone")
		return nil
	}
	h.api.deleteInstance = func(context.Context, int) error {
		t.Fatal("DeleteInstance must be skipped when the old instance is already gone")
		return nil
	}
	h.api.createInstance = func(_ context.Context, opts linode.CreateOptions) (linode.Instance, error) {
		createOpts = opts
		return linode.Instance{ID: 99, Status: "running"}, nil
	}
	h.api.getIPs = func(_ context.Context, id int) (linode.InstanceIPs, error) {
		var ips linode.InstanceIPs
		ips.IPv4.Public = []linode.IPAddress{{Address: "203.0.113.7", Public: true}}
		return ips, nil
	}
	h.mgr.RunFn = func(context.Context, sshrun.Options, string) (sshrun.Result, error) {
		return sshrun.Result{Exit: 0}, nil
	}
	h.mgr.ResolveInstance = func(id int) (linode.Instance, bool) {
		if id != 42 {
			return linode.Instance{}, false
		}
		return linode.Instance{
			ID: 42, Label: "web", Region: "us-east", Type: "g6-standard-1",
			Image: "linode/debian12", Tags: []string{"prod"},
		}, true
	}

	id := startRebuild(t, h.mgr, 42, "echo hi")
	task := h.mgr.runToCompletion(t, id)
	if task.Status != StatusSuccess {
		t.Fatalf("status = %s, want success (err=%q log=%v)", task.Status, task.Err, task.Log)
	}
	if task.NewInstanceID != 99 || task.NewIP != "203.0.113.7" {
		t.Fatalf("task = %+v", task)
	}
	if createOpts.Region != "us-east" || createOpts.Type != "g6-standard-1" ||
		createOpts.Image != "linode/debian12" || createOpts.Label != "web" {
		t.Fatalf("CreateInstance got wrong opts: %+v", createOpts)
	}
	if len(createOpts.Tags) != 1 || createOpts.Tags[0] != "prod" {
		t.Fatalf("CreateInstance tags = %v", createOpts.Tags)
	}
}

// TestRebuildOldInstanceNon404StillFails verifies only a 404 triggers the
// cached-spec fallback; other errors still fail the task.
func TestRebuildOldInstanceNon404StillFails(t *testing.T) {
	h := newRebuildHarness(t)
	h.api.getInstance = func(context.Context, int) (linode.Instance, error) {
		return linode.Instance{}, &linode.APIError{StatusCode: http.StatusInternalServerError, Reason: "boom"}
	}
	h.mgr.ResolveInstance = func(int) (linode.Instance, bool) {
		return linode.Instance{Region: "us-east", Type: "t", Image: "i"}, true
	}

	id := startRebuild(t, h.mgr, 42, "echo hi")
	task := h.mgr.runToCompletion(t, id)
	if task.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", task.Status)
	}
	if !strings.Contains(task.Err, "读取旧实例失败") {
		t.Fatalf("Err = %q", task.Err)
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

	id := startRebuild(t, h.mgr, 42, "echo hi")
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

	id := startRebuild(t, h.mgr, 42, "echo hi")
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

	id := startRebuild(t, h.mgr, 42, "bad script")
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

	id := startRebuild(t, h.mgr, 42, "echo hi")
	task := h.mgr.runToCompletion(t, id)
	if task.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", task.Status)
	}
	if !strings.Contains(task.Err, "SSH 执行开机脚本失败") || !strings.Contains(task.Err, "connection refused") {
		t.Fatalf("Err = %q", task.Err)
	}
}

// TestRebuildSSHConnectErrorRetriesThenSucceeds verifies a connect failure
// (sshd not listening yet) is retried and the task still succeeds.
func TestRebuildSSHConnectErrorRetriesThenSucceeds(t *testing.T) {
	h := newStatefulHarness(t)
	h.mgr.BootScriptAttempts = 3
	h.mgr.BootScriptRetryDelay = 0
	attempts := 0
	h.mgr.RunFn = func(_ context.Context, _ sshrun.Options, _ string) (sshrun.Result, error) {
		attempts++
		if attempts < 3 {
			return sshrun.Result{}, errors.New("sshrun: dial 203.0.113.7:22: connect: connection refused")
		}
		return sshrun.Result{Exit: 0}, nil
	}

	id := startRebuild(t, h.mgr, 42, "echo hi")
	task := h.mgr.runToCompletion(t, id)
	if task.Status != StatusSuccess {
		t.Fatalf("status = %s, want success (err=%q log=%v)", task.Status, task.Err, task.Log)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

// TestRebuildSSHConnectErrorExhaustsRetries verifies connect failures stop
// after BootScriptAttempts attempts and fail the task.
func TestRebuildSSHConnectErrorExhaustsRetries(t *testing.T) {
	h := newStatefulHarness(t)
	h.mgr.BootScriptAttempts = 3
	h.mgr.BootScriptRetryDelay = 0
	attempts := 0
	h.mgr.RunFn = func(_ context.Context, _ sshrun.Options, _ string) (sshrun.Result, error) {
		attempts++
		return sshrun.Result{}, errors.New("sshrun: dial 203.0.113.7:22: connect: connection refused")
	}

	id := startRebuild(t, h.mgr, 42, "echo hi")
	task := h.mgr.runToCompletion(t, id)
	if task.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", task.Status)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	if !strings.Contains(task.Err, "SSH 执行开机脚本失败") || !strings.Contains(task.Err, "connection refused") {
		t.Fatalf("Err = %q", task.Err)
	}
}

// TestManagerConcurrency verifies rebuilds are serialized per instance: a
// second Start for the same instance returns ErrBusy, while a different
// instance may start concurrently.
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
	// Second Start for the same instance while busy.
	if _, err := h.mgr.Start(42, "echo hi", "", "", ""); !errors.Is(err, ErrBusy) {
		t.Fatalf("same-instance Start err = %v, want ErrBusy", err)
	}
	// A different instance may start concurrently.
	id2, err := h.mgr.Start(43, "echo hi", "", "", "")
	if err != nil {
		t.Fatalf("different-instance Start: %v", err)
	}
	close(release)
	task := h.mgr.runToCompletion(t, id)
	if task.Status != StatusFailed {
		t.Fatalf("status = %s", task.Status)
	}
	task2 := h.mgr.runToCompletion(t, id2)
	if task2.Status != StatusFailed {
		t.Fatalf("status = %s", task2.Status)
	}
	// after completion a new Start on the same instance must succeed
	if id3, err := h.mgr.Start(42, "echo hi", "", "", ""); err != nil {
		t.Fatalf("Start after completion: %v", err)
	} else {
		h.mgr.runToCompletion(t, id3)
	}
}

// TestTaskListNewestFirst verifies List orders newest first and reverses logs.
func TestTaskListNewestFirst(t *testing.T) {
	h := newRebuildHarness(t)
	h.api.getInstance = func(_ context.Context, id int) (linode.Instance, error) {
		return linode.Instance{ID: id, Region: "r", Type: "t", Image: "i"}, nil
	}
	id1 := startRebuild(t, h.mgr, 42, "s1")
	h.mgr.runToCompletion(t, id1)
	id2 := startRebuild(t, h.mgr, 43, "s2")
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
	s.Tasks.RepointCache = s.repointInstanceCache
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
	body := `{"script":""}`
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

// TestRebuildInheritsInstanceConfig verifies the stable-cache-id design: after
// a delete+recreate rebuild the new Linode id inherits the old instance's
// boot script and auto-rebuild configuration.
func TestRebuildInheritsInstanceConfig(t *testing.T) {
	h := newStatefulHarness(t)
	h.mgr.RunFn = func(context.Context, sshrun.Options, string) (sshrun.Result, error) {
		return sshrun.Result{}, nil
	}
	var createdOpts linode.CreateOptions
	inner := h.api.createInstance
	h.api.createInstance = func(ctx context.Context, opts linode.CreateOptions) (linode.Instance, error) {
		createdOpts = opts
		return inner(ctx, opts)
	}
	srv, session := rebuildServer(t, h)
	tok := srv.Store.TokensOf(1)[0]
	if err := srv.Store.CacheInstances(tok.ID, []linode.Instance{{ID: 42, Label: "web"}}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Store.SetInstanceScript(42, "echo inherited"); err != nil {
		t.Fatal(err)
	}
	if err := srv.Store.SetInstanceRootPassword(42, "Secret123!"); err != nil {
		t.Fatal(err)
	}
	if err := srv.Store.SetAutoRebuild(store.AutoRebuild{
		InstanceID: 42, UserID: 1, TokenID: tok.ID, Enabled: true,
		Port: 22, Threshold: 3, IntervalSec: 300,
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/instances/42/rebuild", bytes.NewBufferString(`{}`))
	authed(req, session)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	wantStatus(t, rr, http.StatusAccepted)
	var resp rebuildResponse
	decodeJSON(t, rr, &resp)
	task := h.mgr.runToCompletion(t, resp.TaskID)
	if task.Status != StatusSuccess {
		t.Fatalf("task = %+v", task)
	}
	newID := task.NewInstanceID
	if newID <= 0 || newID == 42 {
		t.Fatalf("new instance id = %d", newID)
	}

	// The cached snapshot points at the new instance with the same row.
	cached := srv.Store.CachedInstances(tok.ID)
	if len(cached) != 1 || cached[0].ID != newID {
		t.Fatalf("cached instances = %+v, want only id %d", cached, newID)
	}
	if script, ok := srv.Store.InstanceScript(newID); !ok || script != "echo inherited" {
		t.Fatalf("InstanceScript(%d) = %q, %v", newID, script, ok)
	}
	if cfg, ok := srv.Store.AutoRebuild(newID); !ok || !cfg.Enabled {
		t.Fatalf("AutoRebuild(%d) = %+v, %v", newID, cfg, ok)
	}
	// The replacement instance is created with the inherited root password.
	if createdOpts.RootPass != "Secret123!" {
		t.Fatalf("CreateInstance RootPass = %q, want inherited password", createdOpts.RootPass)
	}
	if pass, ok := srv.Store.InstanceRootPassword(newID); !ok || pass != "Secret123!" {
		t.Fatalf("InstanceRootPassword(%d) = %q, %v", newID, pass, ok)
	}
}

// TestInstanceRootPasswordHTTP covers the per-instance root password
// endpoints (set / check / clear, never echoing the secret).
func TestInstanceRootPasswordHTTP(t *testing.T) {
	h := newRebuildHarness(t)
	srv, session := rebuildServer(t, h)

	get := func() map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/api/instances/42/root-password", nil)
		authed(req, session)
		rr := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rr, req)
		wantStatus(t, rr, http.StatusOK)
		var got map[string]any
		decodeJSON(t, rr, &got)
		return got
	}
	if got := get(); got["configured"] != false {
		t.Fatalf("initial = %+v", got)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/instances/42/root-password",
		bytes.NewBufferString(`{"password":"Secret123!"}`))
	authed(req, session)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	wantStatus(t, rr, http.StatusOK)
	if resp := get(); resp["configured"] != true {
		t.Fatalf("after put = %+v", resp)
	} else if _, leaked := resp["password"]; leaked {
		t.Fatal("GET must not return the password")
	}
	if pass, ok := srv.Store.InstanceRootPassword(42); !ok || pass != "Secret123!" {
		t.Fatalf("stored password = %q, %v", pass, ok)
	}

	// Empty password is rejected.
	req = httptest.NewRequest(http.MethodPut, "/api/instances/42/root-password",
		bytes.NewBufferString(`{"password":"  "}`))
	authed(req, session)
	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	wantStatus(t, rr, http.StatusBadRequest)

	// Delete clears it.
	req = httptest.NewRequest(http.MethodDelete, "/api/instances/42/root-password", nil)
	authed(req, session)
	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	wantStatus(t, rr, http.StatusOK)
	if got := get(); got["configured"] != false {
		t.Fatalf("after delete = %+v", got)
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
	// Second request for the same instance while its task is running → conflict.
	req2 := httptest.NewRequest(http.MethodPost, "/api/instances/42/rebuild", bytes.NewBufferString(body))
	authed(req2, session)
	rr2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr2, req2)
	wantStatus(t, rr2, http.StatusConflict)

	// A different instance may rebuild concurrently.
	req3 := httptest.NewRequest(http.MethodPost, "/api/instances/43/rebuild", bytes.NewBufferString(body))
	authed(req3, session)
	rr3 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr3, req3)
	wantStatus(t, rr3, http.StatusAccepted)
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

	id := startRebuild(t, h.mgr, 42, "echo hi")
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
