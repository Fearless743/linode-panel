package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"

	"linode-panel/internal/linode"
)

// itoa is a local copy for test use.
func itoa(n int) string {
	return strconv.Itoa(n)
}

// --- fakePanel: httptest server speaking the frozen agent protocol ---

type fakePanel struct {
	*httptest.Server
	mu       sync.Mutex
	code     string
	token    string
	nextID   int64
	pending  []pollCommand
	notify   chan struct{}
	results  []map[string]any
	regCalls int

	// failResults>0 makes the next N result reports return 500 (retry test).
	failResults int
	// validToken when false makes poll/result reject auth (revocation test).
	validToken bool
}

func newFakePanel(code, token string) *fakePanel {
	p := &fakePanel{code: code, token: token, validToken: true, notify: make(chan struct{}, 1)}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/agents/register", p.handleRegister)
	mux.HandleFunc("/api/agent/poll", p.handlePoll)
	mux.HandleFunc("/api/agent/result", p.handleResult)
	p.Server = httptest.NewServer(mux)
	return p
}

func (p *fakePanel) handleRegister(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"registration_code"`
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	p.mu.Lock()
	p.regCalls++
	p.mu.Unlock()
	if body.Code != p.code {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "注册码无效"})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"agent_id":    1,
		"agent_token": p.token,
	})
}

func (p *fakePanel) authed(r *http.Request) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.validToken && strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") == p.token
}

func (p *fakePanel) handlePoll(w http.ResponseWriter, r *http.Request) {
	if !p.authed(r) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	// Long-poll: wait up to 1s for a command, or return empty.
	p.mu.Lock()
	for len(p.pending) == 0 {
		p.mu.Unlock()
		select {
		case <-p.notify:
			p.mu.Lock()
		case <-time.After(500 * time.Millisecond):
			_ = json.NewEncoder(w).Encode(map[string]any{"commands": []pollCommand{}})
			return
		}
	}
	cmds := p.pending
	p.pending = nil
	p.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"commands": cmds})
}

func (p *fakePanel) handleResult(w http.ResponseWriter, r *http.Request) {
	if !p.authed(r) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var res map[string]any
	_ = json.Unmarshal(raw, &res)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failResults > 0 {
		p.failResults--
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	p.results = append(p.results, res)
	_ = json.NewEncoder(w).Encode(map[string]any{})
}

// enqueue queues a command and wakes a blocked poll.
func (p *fakePanel) enqueue(typ string, params map[string]any) int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextID++
	p.pending = append(p.pending, pollCommand{ID: p.nextID, Type: typ, Params: params})
	select {
	case p.notify <- struct{}{}:
	default:
	}
	return p.nextID
}

// lastResult returns the most recent result payload (or nil).
func (p *fakePanel) lastResult() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.results) == 0 {
		return nil
	}
	return p.results[len(p.results)-1]
}

func (p *fakePanel) resultsCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.results)
}

// --- fakeLinodeAPI: stateful Linode API v4 fake ---

type fakeLinodeAPI struct {
	mu        sync.Mutex
	status    map[int]string
	instances map[int]linode.Instance
	calls     []string
	nextID    int
	region    string
	typ       string
	image     string
	publicIP  string
}

func newFakeLinodeAPI() *fakeLinodeAPI {
	return &fakeLinodeAPI{
		status:    map[int]string{},
		instances: map[int]linode.Instance{},
		nextID:    100,
		region:    "us-east",
		typ:       "g6-standard-1",
		image:     "linode/debian12",
		publicIP:  "203.0.113.7",
	}
}

func (f *fakeLinodeAPI) record(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
}

// sequence returns recorded call names.
func (f *fakeLinodeAPI) sequence() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

// seed registers an instance with the given status.
func (f *fakeLinodeAPI) seed(id int, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status[id] = status
	f.instances[id] = linode.Instance{
		ID: id, Label: "linode-" + itoa(id), Status: status,
		Region: f.region, Type: f.typ, Image: f.image,
		IPv4: []string{"198.51.100.5"},
	}
}

func (f *fakeLinodeAPI) ListInstances(_ context.Context) ([]linode.Instance, error) {
	f.record("ListInstances")
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []linode.Instance
	for _, in := range f.instances {
		in.Status = f.status[in.ID]
		out = append(out, in)
	}
	return out, nil
}

func (f *fakeLinodeAPI) GetInstance(_ context.Context, id int) (linode.Instance, error) {
	f.record("GetInstance")
	f.mu.Lock()
	defer f.mu.Unlock()
	in, ok := f.instances[id]
	if !ok {
		return linode.Instance{}, &linode.APIError{StatusCode: 404, Reason: "not found"}
	}
	in.Status = f.status[id]
	return in, nil
}

func (f *fakeLinodeAPI) Boot(_ context.Context, id int) error {
	f.record("Boot")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status[id] = "running"
	return nil
}

func (f *fakeLinodeAPI) Shutdown(_ context.Context, id int) error {
	f.record("Shutdown")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status[id] = "offline"
	return nil
}

func (f *fakeLinodeAPI) Reboot(_ context.Context, id int) error {
	f.record("Reboot")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status[id] = "running"
	return nil
}

func (f *fakeLinodeAPI) GetInstanceIPs(_ context.Context, id int) (linode.InstanceIPs, error) {
	f.record("GetInstanceIPs")
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.instances[id]; !ok {
		return linode.InstanceIPs{}, &linode.APIError{StatusCode: 404}
	}
	var ips linode.InstanceIPs
	ips.IPv4.Public = []linode.IPAddress{{Address: f.publicIP, Public: true}}
	return ips, nil
}

func (f *fakeLinodeAPI) CreateInstance(_ context.Context, opts linode.CreateOptions) (linode.Instance, error) {
	f.record("CreateInstance")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	id := f.nextID
	f.status[id] = "running"
	f.instances[id] = linode.Instance{
		ID: id, Label: "new-linode", Status: "running",
		Region: opts.Region, Type: opts.Type, Image: opts.Image,
	}
	return f.instances[id], nil
}

func (f *fakeLinodeAPI) DeleteInstance(_ context.Context, id int) error {
	f.record("DeleteInstance")
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.instances, id)
	delete(f.status, id)
	return nil
}

// waitFor polls cond every 10ms until it returns true or the timeout elapses.
func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}
