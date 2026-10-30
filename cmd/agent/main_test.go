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

	"linode-panel/internal/agentcrypto"
	"linode-panel/internal/linode"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// newTestAgent wires a fake panel + fake Linode API into an agent with a temp
// state file and fast seams.
func newTestAgent(t *testing.T, panel *fakePanel, fake *fakeLinodeAPI, code, statePath string) *agent {
	t.Helper()
	key := agentcrypto.DeriveKey(code)
	pc := newPanelClient(panel.URL, "")
	ex := &executor{
		apiBase: panel.URL + "/linode",
		sshPort: 22,
		run: func(ctx context.Context, host string, port int, privPEM []byte, script string) error {
			return nil
		},
		probe: func(ctx context.Context, host string, port int) error { return nil },
		log:   testLogger(),
	}
	// Replace newAPI so the executor talks to the fake Linode API.
	ex.newAPI = func(pat string) linodeAPI { return fake }
	a := &agent{
		pc:        pc,
		st:        &state{},
		statePath: statePath,
		key:       key,
		code:      code,
		hostname:  "testhost",
		exec:      ex,
		log:       testLogger(),
	}
	return a
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
	// The registration code is persisted so a restart without --code can
	// still derive the PAT encryption key.
	if reloaded.RegistrationCode != "regcode" {
		t.Fatalf("persisted code = %q, want regcode", reloaded.RegistrationCode)
	}
}

func TestAdoptTokenPersistsToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	st, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := adoptToken(&st, path, "tok-abc", "regcode"); err != nil {
		t.Fatalf("adoptToken: %v", err)
	}
	if st.AgentToken != "tok-abc" || st.RegistrationCode != "regcode" {
		t.Fatalf("state = %+v, want token=tok-abc code=regcode", st)
	}
	reloaded, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.AgentToken != "tok-abc" {
		t.Fatalf("persisted token = %q, want tok-abc", reloaded.AgentToken)
	}
	// A second adoption must not clobber the registered identity.
	if err := adoptToken(&reloaded, path, "other-tok", "other-code"); err != nil {
		t.Fatalf("adoptToken again: %v", err)
	}
	if reloaded.AgentToken != "tok-abc" || reloaded.RegistrationCode != "regcode" {
		t.Fatalf("state = %+v, want identity kept", reloaded)
	}
}

func TestAdoptTokenNoOpWithoutFlag(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	st := &state{}
	if err := adoptToken(st, path, "", "regcode"); err != nil {
		t.Fatalf("adoptToken: %v", err)
	}
	if st.AgentToken != "" {
		t.Fatalf("token = %q, want empty", st.AgentToken)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("state file written without a token: err = %v", err)
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

func TestSetPATDecryptsAndPersists(t *testing.T) {
	code := "regcode"
	key := agentcrypto.DeriveKey(code)
	panel := newFakePanel(code, "tok")
	defer panel.Close()
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	fake := newFakeLinodeAPI()
	a := newTestAgent(t, panel, fake, code, statePath)
	a.st.AgentToken = "tok"
	a.pc.token = "tok"

	sealed, err := agentcrypto.Seal(key, []byte("pat-abc"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := pollCommand{ID: 1, Type: cmdSetPAT, Params: map[string]any{"sealed_pat": sealed}}
	if err := a.execute(context.Background(), cmd); err != nil {
		t.Fatalf("execute set_pat: %v", err)
	}
	if len(a.exec.pats) != 1 || a.exec.pats[0] != "pat-abc" {
		t.Fatalf("pats = %v, want [pat-abc]", a.exec.pats)
	}
	// The state file must hold the sealed PAT encrypted at rest.
	reloaded, err := loadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.SealedPAT == "" {
		t.Fatal("sealed PAT not persisted")
	}
	plain, err := agentcrypto.Open(key, reloaded.SealedPAT)
	if err != nil || string(plain) != "pat-abc" {
		t.Fatalf("reload open = %q err=%v, want pat-abc", plain, err)
	}
	// A final result must have been reported.
	res := panel.lastResult()
	if res == nil {
		t.Fatal("no result reported")
	}
	if res["ok"] != true || res["command_id"] != float64(1) {
		t.Fatalf("result = %v, want ok command 1", res)
	}
}

func TestPollAndExecutePower(t *testing.T) {
	code := "regcode"
	panel := newFakePanel(code, "tok")
	defer panel.Close()
	dir := t.TempDir()
	fake := newFakeLinodeAPI()
	fake.seed(42, "running")
	a := newTestAgent(t, panel, fake, code, filepath.Join(dir, "state.json"))
	a.st.AgentToken = "tok"
	a.pc.token = "tok"
	a.exec.pats = []string{"pat-1"}

	// Run the loop in the background.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- a.run(ctx) }()

	panel.enqueue("power", map[string]any{"action": "shutdown", "id": 42})
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

// TestAuthLossReregisters verifies that a 401 on poll clears the token and
// re-registers using the stored registration code.
func TestAuthLossReregisters(t *testing.T) {
	code := "regcode"
	panel := newFakePanel(code, "tok")
	defer panel.Close()
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	fake := newFakeLinodeAPI()
	a := newTestAgent(t, panel, fake, code, statePath)
	a.st.AgentToken = "tok"
	a.st.RegistrationCode = code
	a.pc.token = "tok"
	a.exec.pats = []string{"pat-1"}
	a.code = code

	// Revoke the token: poll returns 401.
	panel.mu.Lock()
	panel.validToken = false
	panel.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- a.run(ctx) }()

	// The agent must re-register (register returns the token) and resume
	// polling; once valid again, a queued command executes.
	if !waitFor(3*time.Second, func() bool {
		panel.mu.Lock()
		defer panel.mu.Unlock()
		return panel.regCalls >= 1
	}) {
		t.Fatal("agent did not re-register")
	}
	panel.mu.Lock()
	panel.validToken = true
	panel.mu.Unlock()
	panel.enqueue("power", map[string]any{"action": "reboot", "id": 42})
	if !waitFor(3*time.Second, func() bool { return panel.resultsCount() >= 1 }) {
		t.Fatal("no result after re-registration")
	}
	res := panel.lastResult()
	if res["ok"] != true {
		t.Fatalf("result = %v, want ok", res)
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
	a := newTestAgent(t, panel, fake, code, filepath.Join(dir, "state.json"))
	a.st.AgentToken = "tok"
	a.pc.token = "tok"
	a.exec.pats = []string{"pat-1"}

	cmd := pollCommand{ID: 5, Type: "list", Params: map[string]any{}}
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

// TestListNoPATs verifies list with no local PAT returns an empty array (not
// null / not an error).
func TestListNoPATs(t *testing.T) {
	code := "regcode"
	panel := newFakePanel(code, "tok")
	defer panel.Close()
	dir := t.TempDir()
	fake := newFakeLinodeAPI()
	a := newTestAgent(t, panel, fake, code, filepath.Join(dir, "state.json"))
	a.st.AgentToken = "tok"
	a.pc.token = "tok"
	// no pats
	cmd := pollCommand{ID: 6, Type: "list", Params: map[string]any{}}
	if err := a.execute(context.Background(), cmd); err != nil {
		t.Fatalf("execute list: %v", err)
	}
	res := panel.lastResult()
	if res["ok"] != true {
		t.Fatalf("result = %v, want ok", res)
	}
	payload, _ := res["payload"].(map[string]any)
	insts, ok := payload["instances"].([]any)
	if !ok || len(insts) != 0 {
		t.Fatalf("instances = %v (ok=%v), want empty array", insts, ok)
	}
}

// TestRebuildProgressReports verifies in-flight progress updates carry
// ok=false with a payload.progress string, distinct from the final result.
func TestRebuildProgressReports(t *testing.T) {
	code := "regcode"
	key := agentcrypto.DeriveKey(code)
	panel := newFakePanel(code, "tok")
	defer panel.Close()
	dir := t.TempDir()
	fake := newFakeLinodeAPI()
	fake.seed(7, "running")
	a := newTestAgent(t, panel, fake, code, filepath.Join(dir, "state.json"))
	a.st.AgentToken = "tok"
	a.pc.token = "tok"
	a.exec.pats = []string{"pat-1"}
	sealed, _ := agentcrypto.Seal(key, []byte("pat-1"))
	cmd := pollCommand{ID: 11, Type: "rebuild", Params: map[string]any{
		"id": 7, "script": "echo boot", "sealed_pat": sealed,
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
	key := agentcrypto.DeriveKey(code)
	panel := newFakePanel(code, "tok")
	defer panel.Close()
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	fake := newFakeLinodeAPI()
	fake.seed(7, "running")
	a := newTestAgent(t, panel, fake, code, statePath)
	a.st.AgentToken = "tok"
	a.pc.token = "tok"
	a.exec.pats = []string{"pat-1"}

	sealed, err := agentcrypto.Seal(key, []byte("pat-1"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := pollCommand{ID: 9, Type: "rebuild", Params: map[string]any{
		"id": 7, "script": "echo boot", "reserved_ip": "203.0.113.50", "sealed_pat": sealed,
	}}
	if err := a.execute(context.Background(), cmd); err != nil {
		t.Fatalf("execute rebuild: %v", err)
	}
	res := panel.lastResult()
	if res["ok"] != true {
		t.Fatalf("result = %v, want ok", res)
	}
	// Verify the orchestration sequence: GetInstance, Shutdown, GetConfigID,
	// UpdateConfigNetworkHelper, DeleteInstance, CreateInstance, Boot, etc.
	seq := fake.sequence()
	for _, want := range []string{"Shutdown", "DeleteInstance", "CreateInstance", "Boot", "GetInstanceIPs"} {
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

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestRebuildSealedPATError(t *testing.T) {
	code := "regcode"
	panel := newFakePanel(code, "tok")
	defer panel.Close()
	dir := t.TempDir()
	fake := newFakeLinodeAPI()
	a := newTestAgent(t, panel, fake, code, filepath.Join(dir, "state.json"))
	a.st.AgentToken = "tok"
	a.pc.token = "tok"

	cmd := pollCommand{ID: 3, Type: "rebuild", Params: map[string]any{
		"id": 7, "script": "x", "sealed_pat": "not-valid-base64!",
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
	a := newTestAgent(t, panel, fake, code, filepath.Join(dir, "state.json"))
	a.st.AgentToken = "tok"
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

// --- power action across PATs (404/403 skip) ---

func TestPowerActionSkipsForbiddenPAT(t *testing.T) {
	// First PAT returns 404; second succeeds.
	fake := newFakeLinodeAPI()
	fake.seed(42, "running")
	ex := &executor{
		apiBase: "http://x",
		sshPort: 22,
		run:     func(ctx context.Context, h string, p int, k []byte, s string) error { return nil },
		probe:   func(ctx context.Context, h string, p int) error { return nil },
		log:     testLogger(),
	}
	// Override newAPI: first call returns a 404 API, second the real fake.
	calls := 0
	var mu sync.Mutex
	ex.newAPI = func(pat string) linodeAPI {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			return &errLinodeAPI{err: &linode.APIError{StatusCode: 404}}
		}
		return fake
	}
	ex.pats = []string{"pat-bad", "pat-good"}
	if err := ex.powerAction(context.Background(), "boot", 42); err != nil {
		t.Fatalf("powerAction: %v", err)
	}
	if !fakeCalled(fake, "Boot") {
		t.Fatal("fake did not Boot")
	}
}

// errLinodeAPI always returns err for every method.
type errLinodeAPI struct{ err error }

func (e *errLinodeAPI) ListInstances(context.Context) ([]linode.Instance, error) { return nil, e.err }

func (e *errLinodeAPI) GetInstance(context.Context, int) (linode.Instance, error) {
	return linode.Instance{}, e.err
}
func (e *errLinodeAPI) Boot(context.Context, int) error     { return e.err }
func (e *errLinodeAPI) Shutdown(context.Context, int) error { return e.err }
func (e *errLinodeAPI) Reboot(context.Context, int) error   { return e.err }
func (e *errLinodeAPI) GetInstanceIPs(context.Context, int) (linode.InstanceIPs, error) {
	return linode.InstanceIPs{}, e.err
}

func (e *errLinodeAPI) CreateInstance(context.Context, linode.CreateOptions) (linode.Instance, error) {
	return linode.Instance{}, e.err
}
func (e *errLinodeAPI) DeleteInstance(context.Context, int) error { return e.err }
func (e *errLinodeAPI) GetConfigID(context.Context, int) (int, error) {
	return 0, e.err
}

func (e *errLinodeAPI) UpdateConfigNetworkHelper(context.Context, int, int, bool) error {
	return e.err
}
func (e *errLinodeAPI) AssignIP(context.Context, string, string, int) error { return e.err }
func (e *errLinodeAPI) AttachAllowAllFirewall(context.Context, string, int) error {
	return e.err
}
