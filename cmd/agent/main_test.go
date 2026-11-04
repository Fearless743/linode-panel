package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"linode-panel/internal/agenthub"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// newTestAgent wires a fake panel + fake Linode API into an agent with fast
// seams.
func newTestAgent(t *testing.T, panel *fakePanel, fake *fakeLinodeAPI, statePath string) *agent {
	t.Helper()
	pc := newPanelClient(panel.URL, "")
	ex := &agenthub.Executor{
		APIBase: panel.URL + "/linode",
		SSHPort: 22,
		Run: func(ctx context.Context, host string, port int, password string, script string) error {
			return nil
		},
		Probe: func(ctx context.Context, host string, port int) error { return nil },
		Log:   testLogger(),
	}
	// Replace NewAPI so the executor talks to the fake Linode API.
	ex.NewAPI = func(pat string) agenthub.LinodeAPI { return fake }
	return &agent{
		pc:       pc,
		hostname: "testhost",
		exec:     ex,
		log:      testLogger(),
	}
}

func testLogger() *slog.Logger { return discardLogger() }

// --- tests ---

func TestRegisterPersistsToken(t *testing.T) {
	panel := newFakePanel("regcode", "tok123")
	defer panel.Close()
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")

	st, err := loadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	pc := newPanelClient(panel.URL, "")
	if err := ensureToken(context.Background(), &st, statePath, pc, "regcode", "host1"); err != nil {
		t.Fatalf("ensureToken: %v", err)
	}
	if st.AgentToken != "tok123" || st.AgentID != 1 {
		t.Fatalf("state = %+v, want token=tok123 id=1", st)
	}
	// State file must persist it.
	reloaded, err := loadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.AgentToken != "tok123" {
		t.Fatalf("persisted token = %q, want tok123", reloaded.AgentToken)
	}
}

func TestRegisterWrongCode(t *testing.T) {
	panel := newFakePanel("regcode", "tok123")
	defer panel.Close()
	dir := t.TempDir()
	pc := newPanelClient(panel.URL, "")
	st := &state{}
	err := ensureToken(context.Background(), st, filepath.Join(dir, "s.json"), pc, "wrong", "h")
	if err == nil || !strings.Contains(err.Error(), "注册") {
		t.Fatalf("err = %v, want registration failure", err)
	}
}

func TestPollAndExecutePower(t *testing.T) {
	code := "regcode"
	panel := newFakePanel(code, "tok")
	defer panel.Close()
	dir := t.TempDir()
	fake := newFakeLinodeAPI()
	fake.seed(42, "running")
	a := newTestAgent(t, panel, fake, filepath.Join(dir, "state.json"))
	a.pc.token = "tok"

	// Run the loop in the background.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- a.run(ctx) }()

	panel.enqueue("power", map[string]any{"action": "shutdown", "id": 42, "pat": "pat-1"})
	if !waitFor(3*time.Second, func() bool { return panel.resultsCount() >= 1 }) {
		t.Fatal("no result within timeout")
	}
	res := panel.lastResult()
	if res["ok"] != true {
		t.Fatalf("result = %v, want ok", res)
	}
	// The fake must have recorded Shutdown.
	if !fakeCalled(fake, "Shutdown") {
		t.Fatalf("fake calls = %v, want Shutdown", fake.sequence())
	}
}

// TestAuthLossStops verifies that a 401 on poll stops the agent (no silent
// token re-registration) and surfaces a fatal error.
func TestAuthLossStops(t *testing.T) {
	code := "regcode"
	panel := newFakePanel(code, "tok")
	defer panel.Close()
	dir := t.TempDir()
	fake := newFakeLinodeAPI()
	a := newTestAgent(t, panel, fake, filepath.Join(dir, "state.json"))
	a.pc.token = "tok"

	// Revoke the token: poll returns 401.
	panel.mu.Lock()
	panel.validToken = false
	panel.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- a.run(ctx) }()

	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), "面板拒绝身份") {
			t.Fatalf("err = %v, want panel rejection", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("agent did not stop on auth loss")
	}
	// It must not have tried to re-register.
	panel.mu.Lock()
	defer panel.mu.Unlock()
	if panel.regCalls != 0 {
		t.Fatalf("regCalls = %d, want 0 (no re-registration)", panel.regCalls)
	}
}

func fakeCalled(f *fakeLinodeAPI, name string) bool {
	for _, c := range f.sequence() {
		if c == name {
			return true
		}
	}
	return false
}

func TestListCommandAggregates(t *testing.T) {
	code := "regcode"
	panel := newFakePanel(code, "tok")
	defer panel.Close()
	dir := t.TempDir()
	fake := newFakeLinodeAPI()
	fake.seed(1, "running")
	fake.seed(2, "offline")
	a := newTestAgent(t, panel, fake, filepath.Join(dir, "state.json"))
	a.pc.token = "tok"

	cmd := pollCommand{ID: 5, Type: "list", Params: map[string]any{"pat": "pat-1"}}
	if err := a.execute(context.Background(), cmd); err != nil {
		t.Fatalf("execute list: %v", err)
	}
	res := panel.lastResult()
	if res["ok"] != true {
		t.Fatalf("result = %v, want ok", res)
	}
	payload, _ := res["payload"].(map[string]any)
	insts, _ := payload["instances"].([]any)
	if len(insts) != 2 {
		t.Fatalf("instances = %d, want 2", len(insts))
	}
}

// TestListNoPAT verifies a list command without a PAT fails cleanly.
func TestListNoPAT(t *testing.T) {
	code := "regcode"
	panel := newFakePanel(code, "tok")
	defer panel.Close()
	dir := t.TempDir()
	fake := newFakeLinodeAPI()
	a := newTestAgent(t, panel, fake, filepath.Join(dir, "state.json"))
	a.pc.token = "tok"

	cmd := pollCommand{ID: 6, Type: "list", Params: map[string]any{}}
	if err := a.execute(context.Background(), cmd); err != nil {
		t.Fatalf("execute list: %v", err)
	}
	res := panel.lastResult()
	if res["ok"] != false {
		t.Fatalf("result = %v, want not ok", res)
	}
}

// TestRebuildProgressReports verifies in-flight progress updates carry
// ok=false with a payload.progress string, distinct from the final result.
func TestRebuildProgressReports(t *testing.T) {
	code := "regcode"
	panel := newFakePanel(code, "tok")
	defer panel.Close()
	dir := t.TempDir()
	fake := newFakeLinodeAPI()
	fake.seed(7, "running")
	a := newTestAgent(t, panel, fake, filepath.Join(dir, "state.json"))
	a.pc.token = "tok"
	cmd := pollCommand{ID: 11, Type: "rebuild", Params: map[string]any{
		"id": 7, "script": "echo boot", "pat": "pat-1",
	}}
	if err := a.execute(context.Background(), cmd); err != nil {
		t.Fatalf("execute rebuild: %v", err)
	}
	// At least one non-final progress result exists.
	panel.mu.Lock()
	found := false
	for _, r := range panel.results {
		if r["ok"] == false {
			if p, ok := r["payload"].(map[string]any); ok {
				if _, ok := p["progress"].(string); ok {
					found = true
				}
			}
		}
	}
	panel.mu.Unlock()
	if !found {
		t.Fatal("no progress result with payload.progress found")
	}
}

func TestRebuildFullOrchestration(t *testing.T) {
	code := "regcode"
	panel := newFakePanel(code, "tok")
	defer panel.Close()
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	fake := newFakeLinodeAPI()
	fake.seed(7, "running")
	a := newTestAgent(t, panel, fake, statePath)
	a.pc.token = "tok"

	cmd := pollCommand{ID: 9, Type: "rebuild", Params: map[string]any{
		"id": 7, "script": "echo boot", "pat": "pat-1",
	}}
	if err := a.execute(context.Background(), cmd); err != nil {
		t.Fatalf("execute rebuild: %v", err)
	}
	res := panel.lastResult()
	if res["ok"] != true {
		t.Fatalf("result = %v, want ok", res)
	}
	// Verify the orchestration sequence: GetInstance, Shutdown, DeleteInstance,
	// CreateInstance, GetInstanceIPs, etc.
	seq := fake.sequence()
	for _, want := range []string{"Shutdown", "DeleteInstance", "CreateInstance", "GetInstanceIPs"} {
		if !contains(seq, want) {
			t.Fatalf("sequence %v missing %s", seq, want)
		}
	}
	// Progress reports must have been sent (ok=false with progress payload)
	// before the final ok=true result.
	if panel.resultsCount() < 3 {
		t.Fatalf("results = %d, want >=3 (progress + final)", panel.resultsCount())
	}
	// Verify the final result carries the refreshed instance list.
	payload, _ := res["payload"].(map[string]any)
	insts, _ := payload["instances"].([]any)
	if len(insts) < 1 {
		t.Fatalf("final payload instances = %d, want >=1", len(insts))
	}
}

func TestRebuildOldInstanceMissingUsesFallback(t *testing.T) {
	code := "regcode"
	panel := newFakePanel(code, "tok")
	defer panel.Close()
	dir := t.TempDir()
	fake := newFakeLinodeAPI() // instance 7 is not seeded: GetInstance 404s
	a := newTestAgent(t, panel, fake, filepath.Join(dir, "state.json"))
	a.pc.token = "tok"

	cmd := pollCommand{ID: 12, Type: "rebuild", Params: map[string]any{
		"id": 7, "script": "echo boot", "pat": "pat-1",
		"old_instance": map[string]any{
			"region": "us-east", "type": "g6-standard-1", "image": "linode/debian12",
			"label": "web", "tags": []any{"prod"},
		},
	}}
	if err := a.execute(context.Background(), cmd); err != nil {
		t.Fatalf("execute rebuild: %v", err)
	}
	res := panel.lastResult()
	if res["ok"] != true {
		t.Fatalf("result = %v, want ok", res)
	}
	seq := fake.sequence()
	if !contains(seq, "CreateInstance") {
		t.Fatalf("sequence %v missing CreateInstance", seq)
	}
	for _, unwanted := range []string{"Shutdown", "DeleteInstance"} {
		if contains(seq, unwanted) {
			t.Fatalf("sequence %v should not contain %s for a missing instance", seq, unwanted)
		}
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestRebuildMissingPAT(t *testing.T) {
	code := "regcode"
	panel := newFakePanel(code, "tok")
	defer panel.Close()
	dir := t.TempDir()
	fake := newFakeLinodeAPI()
	a := newTestAgent(t, panel, fake, filepath.Join(dir, "state.json"))
	a.pc.token = "tok"

	cmd := pollCommand{ID: 3, Type: "rebuild", Params: map[string]any{
		"id": 7, "script": "x",
	}}
	if err := a.execute(context.Background(), cmd); err != nil {
		t.Fatalf("execute: %v", err)
	}
	res := panel.lastResult()
	if res["ok"] != false {
		t.Fatalf("result = %v, want not ok", res)
	}
}

// --- HTTP-level panel shape test (register/poll/result frozen shapes) ---

func TestPanelProtocolShapes(t *testing.T) {
	var mu sync.Mutex
	var gotRegister map[string]any
	var gotPollAuth string
	var gotResult map[string]any
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/agents/register":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			gotRegister = body
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"agent_id": 7, "agent_token": "tok"})
		case "/api/agent/poll":
			mu.Lock()
			gotPollAuth = r.Header.Get("Authorization")
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"commands": []map[string]any{}})
		case "/api/agent/result":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			gotResult = body
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{})
		}
	}))
	defer panel.Close()

	pc := newPanelClient(panel.URL, "")
	resp, err := pc.register(context.Background(), "code", "host")
	if err != nil || resp.Token != "tok" {
		t.Fatalf("register = %+v err=%v", resp, err)
	}
	mu.Lock()
	if gotRegister["registration_code"] != "code" || gotRegister["name"] != "host" {
		t.Fatalf("register body = %v", gotRegister)
	}
	mu.Unlock()

	pc.token = "tok"
	_, err = pc.poll(context.Background(), "host")
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if gotPollAuth != "Bearer tok" {
		t.Fatalf("poll auth = %q", gotPollAuth)
	}
	mu.Unlock()

	if err := pc.reportResult(context.Background(), 9, true, "", map[string]any{"ok": true}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if gotResult["command_id"] != float64(9) || gotResult["ok"] != true {
		t.Fatalf("result = %v", gotResult)
	}
	mu.Unlock()
}

func TestResultRetryAfterFailure(t *testing.T) {
	code := "regcode"
	panel := newFakePanel(code, "tok")
	defer panel.Close()
	dir := t.TempDir()
	fake := newFakeLinodeAPI()
	a := newTestAgent(t, panel, fake, filepath.Join(dir, "state.json"))
	a.pc.token = "tok"

	panel.mu.Lock()
	panel.failResults = 2 // first two reports fail, third succeeds
	panel.mu.Unlock()

	if err := a.reportWithRetry(1, true, "", map[string]any{}); err != nil {
		t.Fatalf("reportWithRetry: %v", err)
	}
	if panel.resultsCount() != 1 {
		t.Fatalf("results = %d, want 1", panel.resultsCount())
	}
}

func TestFatalMissingCode(t *testing.T) {
	dir := t.TempDir()
	st, err := loadState(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if st.AgentToken == "" {
		// ensureToken with no code must fail with a Chinese error.
		pc := newPanelClient("http://127.0.0.1:1", "")
		err := ensureToken(context.Background(), &st, filepath.Join(dir, "s.json"), pc, "", "h")
		if err == nil || !strings.Contains(err.Error(), "注册码") {
			t.Fatalf("err = %v, want missing code error", err)
		}
	}
}

// --- state file mode test ---

func TestStateFileMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "state.json")
	st := state{AgentID: 3, AgentToken: "tok"}
	if err := saveState(path, st); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", fi.Mode().Perm())
	}
	reloaded, err := loadState(path)
	if err != nil || reloaded.AgentToken != "tok" {
		t.Fatalf("reload = %+v err=%v", reloaded, err)
	}
}

// --- power action (single PAT) ---

func TestPowerSinglePAT(t *testing.T) {
	fake := newFakeLinodeAPI()
	fake.seed(42, "running")
	ex := &agenthub.Executor{
		APIBase: "http://x",
		SSHPort: 22,
		Run:     func(ctx context.Context, h string, p int, pw string, s string) error { return nil },
		Probe:   func(ctx context.Context, h string, p int) error { return nil },
		Log:     testLogger(),
	}
	ex.NewAPI = func(pat string) agenthub.LinodeAPI { return fake }
	if err := ex.Power(context.Background(), "pat-good", "boot", 42); err != nil {
		t.Fatalf("Power: %v", err)
	}
	if !fakeCalled(fake, "Boot") {
		t.Fatal("fake did not Boot")
	}
}
