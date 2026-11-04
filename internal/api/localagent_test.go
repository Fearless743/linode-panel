package api

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"linode-panel/internal/agenthub"
	"linode-panel/internal/linode"
)

// waitForResult polls the Hub until at least one result exists.
func waitForResult(t *testing.T, srv *Server, timeout time.Duration) []agenthub.Result {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if rs := srv.Hub.Results(); len(rs) > 0 {
			return rs
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no result within timeout")
	return nil
}

// resultOK reports whether any result for cmdID is ok.
func resultOK(results []agenthub.Result, cmdID int64) bool {
	for _, r := range results {
		if r.CommandID == cmdID && r.OK {
			return true
		}
	}
	return false
}

func TestSelfAgentRegisteredAndNonDeletable(t *testing.T) {
	srv, cookie := linodeServerFor(t, map[string][]linode.Instance{})
	addAliceToken(t, srv, "main", "pat1")
	if err := srv.Store.SetRegCode("regcode-1"); err != nil {
		t.Fatalf("SetRegCode: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	localID := srv.EnableSelfAgent(ctx, "本地面板")

	// The self-agent appears in the agent list.
	rr := authedGet(t, srv, cookie, "/api/agents")
	wantStatus(t, rr, http.StatusOK)
	var resp struct {
		Agents []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"agents"`
	}
	decodeJSON(t, rr, &resp)
	if len(resp.Agents) != 1 || resp.Agents[0].ID != localID {
		t.Fatalf("agents = %+v, want [local %d]", resp.Agents, localID)
	}
	// The self-agent cannot be deleted.
	rr2 := authedDelete(t, srv, cookie, "/api/agents/"+strconv.Itoa(localID))
	wantStatus(t, rr2, http.StatusBadRequest)
}

// TestSelfAgentCustomName verifies the self-agent honors a custom name.
func TestSelfAgentCustomName(t *testing.T) {
	srv, _ := linodeServerFor(t, map[string][]linode.Instance{})
	addAliceToken(t, srv, "main", "pat1")
	if err := srv.Store.SetRegCode("regcode-1"); err != nil {
		t.Fatalf("SetRegCode: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	localID := srv.EnableSelfAgent(ctx, "我的面板")

	agent, ok := srv.Hub.Agent(localID)
	if !ok || agent.Name != "我的面板" {
		t.Fatalf("self agent = %+v (ok=%v), want name 我的面板", agent, ok)
	}
}

// TestRenameSelfAgent verifies the panel's own agent can be renamed via the
// admin endpoint and that the new name persists in the store.
func TestRenameSelfAgent(t *testing.T) {
	srv, cookie := linodeServerFor(t, map[string][]linode.Instance{})
	if err := srv.Store.SetRegCode("regcode-1"); err != nil {
		t.Fatalf("SetRegCode: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	localID := srv.EnableSelfAgent(ctx, "本地面板")

	rr := authedPut(t, srv, cookie, "/api/agents/"+strconv.Itoa(localID)+"/name", `{"name":"机房面板"}`)
	wantStatus(t, rr, http.StatusOK)

	// The hub and the persisted name both reflect the rename.
	agent, ok := srv.Hub.Agent(localID)
	if !ok || agent.Name != "机房面板" {
		t.Fatalf("agent = %+v (ok=%v), want name 机房面板", agent, ok)
	}
	if got := srv.Store.SelfAgentName(); got != "机房面板" {
		t.Fatalf("SelfAgentName() = %q, want 机房面板", got)
	}

	// Empty names are rejected.
	rr2 := authedPut(t, srv, cookie, "/api/agents/"+strconv.Itoa(localID)+"/name", `{"name":"  "}`)
	wantStatus(t, rr2, http.StatusBadRequest)
	if agent, _ := srv.Hub.Agent(localID); agent.Name != "机房面板" {
		t.Fatalf("name changed after rejected rename: %q", agent.Name)
	}
}

// TestRenameRejectsRemoteAgent verifies remote agents cannot be renamed —
// their name is chosen by the agent process at registration.
func TestRenameRejectsRemoteAgent(t *testing.T) {
	srv, cookie := linodeServerFor(t, map[string][]linode.Instance{})
	if err := srv.Store.SetRegCode("regcode-1"); err != nil {
		t.Fatalf("SetRegCode: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.EnableSelfAgent(ctx, "本地面板")
	remoteID := registerAgentID(t, srv, "regcode-1", "node-1")

	rr := authedPut(t, srv, cookie, "/api/agents/"+strconv.Itoa(remoteID)+"/name", `{"name":"hijacked"}`)
	wantStatus(t, rr, http.StatusBadRequest)
	if agent, _ := srv.Hub.Agent(remoteID); agent.Name != "node-1" {
		t.Fatalf("remote agent renamed to %q", agent.Name)
	}

	// With the self-agent disabled, renaming is rejected outright.
	srv2, cookie2 := linodeServerFor(t, map[string][]linode.Instance{})
	if err := srv2.Store.SetRegCode("regcode-1"); err != nil {
		t.Fatalf("SetRegCode: %v", err)
	}
	remote2 := registerAgentID(t, srv2, "regcode-1", "node-2")
	rr2 := authedPut(t, srv2, cookie2, "/api/agents/"+strconv.Itoa(remote2)+"/name", `{"name":"x"}`)
	wantStatus(t, rr2, http.StatusBadRequest)
}

func TestSelfAgentExecutesPower(t *testing.T) {
	srv, cookie := linodeServerFor(t, map[string][]linode.Instance{
		"pat1": {{ID: 7, Label: "web", Status: "running", IPv4: []string{"10.0.0.1"}}},
	})
	addAliceToken(t, srv, "main", "pat1")
	if err := srv.Store.SetRegCode("regcode-1"); err != nil {
		t.Fatalf("SetRegCode: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	localID := srv.EnableSelfAgent(ctx, "本地面板")

	// via_agent to the local agent: the panel executes the power action itself.
	rr := authedPost(t, srv, cookie, "/api/instances/7/boot?via_agent=1&agent_id="+strconv.Itoa(localID), "")
	wantStatus(t, rr, http.StatusAccepted)
	var resp struct {
		CommandID int64 `json:"command_id"`
	}
	decodeJSON(t, rr, &resp)
	if resp.CommandID <= 0 {
		t.Fatalf("command id = %d", resp.CommandID)
	}
	results := waitForResult(t, srv, 3*time.Second)
	if !resultOK(results, resp.CommandID) {
		t.Fatalf("no ok result for command %d: %+v", resp.CommandID, results)
	}
}

// TestSelfAgentExecutesPowerNoRegCode verifies the panel can act as its own
// agent without a registration code configured (out of the box).
func TestSelfAgentExecutesPowerNoRegCode(t *testing.T) {
	srv, cookie := linodeServerFor(t, map[string][]linode.Instance{
		"pat1": {{ID: 7, Label: "web", Status: "running", IPv4: []string{"10.0.0.1"}}},
	})
	addAliceToken(t, srv, "main", "pat1")
	// No SetRegCode: the panel's own agent must still work.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	localID := srv.EnableSelfAgent(ctx, "本地面板")

	// Default (unbound) via_agent routes to the panel and executes in-process.
	rr := authedPost(t, srv, cookie, "/api/instances/7/boot?via_agent=1", "")
	wantStatus(t, rr, http.StatusAccepted)
	var resp struct {
		CommandID int64 `json:"command_id"`
	}
	decodeJSON(t, rr, &resp)
	if resp.CommandID <= 0 {
		t.Fatalf("command id = %d", resp.CommandID)
	}
	cmd, ok := srv.Hub.Command(resp.CommandID)
	if !ok || cmd.AgentID != localID {
		t.Fatalf("command agent = %+v, want local %d", cmd, localID)
	}
	results := waitForResult(t, srv, 3*time.Second)
	if !resultOK(results, resp.CommandID) {
		t.Fatalf("no ok result for command %d: %+v", resp.CommandID, results)
	}
}

func TestSelfAgentFallbackPrefersLocal(t *testing.T) {
	srv, cookie := linodeServerFor(t, map[string][]linode.Instance{
		"pat1": {{ID: 7, Label: "web", Status: "running"}},
	})
	addAliceToken(t, srv, "main", "pat1")
	if err := srv.Store.SetRegCode("regcode-1"); err != nil {
		t.Fatalf("SetRegCode: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	localID := srv.EnableSelfAgent(ctx, "本地面板")
	registerAgentID(t, srv, "regcode-1", "node-1")

	// Unbound via_agent prefers the panel's own agent over a remote agent.
	rr := authedPost(t, srv, cookie, "/api/instances/7/boot?via_agent=1", "")
	wantStatus(t, rr, http.StatusAccepted)
	var resp struct {
		CommandID int64 `json:"command_id"`
	}
	decodeJSON(t, rr, &resp)
	cmd, ok := srv.Hub.Command(resp.CommandID)
	if !ok || cmd.AgentID != localID {
		t.Fatalf("command agent = %+v, want local %d", cmd, localID)
	}
}

func TestSelfAgentDisabledFallsBackToFirstAgent(t *testing.T) {
	srv, cookie := linodeServerFor(t, map[string][]linode.Instance{
		"pat1": {{ID: 7, Label: "web", Status: "running"}},
	})
	addAliceToken(t, srv, "main", "pat1")
	if err := srv.Store.SetRegCode("regcode-1"); err != nil {
		t.Fatalf("SetRegCode: %v", err)
	}
	remoteID := registerAgentID(t, srv, "regcode-1", "node-1")

	// With the self-agent disabled (--self-agent=false), unbound via_agent
	// falls back to the first registered agent.
	rr := authedPost(t, srv, cookie, "/api/instances/7/boot?via_agent=1", "")
	wantStatus(t, rr, http.StatusAccepted)
	var resp struct {
		CommandID int64 `json:"command_id"`
	}
	decodeJSON(t, rr, &resp)
	cmd, ok := srv.Hub.Command(resp.CommandID)
	if !ok || cmd.AgentID != remoteID {
		t.Fatalf("command agent = %+v, want remote %d", cmd, remoteID)
	}
}

func TestSelfAgentFallbackWhenOnlyLocal(t *testing.T) {
	srv, cookie := linodeServerFor(t, map[string][]linode.Instance{
		"pat1": {{ID: 7, Label: "web", Status: "running"}},
	})
	addAliceToken(t, srv, "main", "pat1")
	if err := srv.Store.SetRegCode("regcode-1"); err != nil {
		t.Fatalf("SetRegCode: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	localID := srv.EnableSelfAgent(ctx, "本地面板")

	// With no remote agent, unbound via_agent falls back to the self-agent.
	rr := authedPost(t, srv, cookie, "/api/instances/7/boot?via_agent=1", "")
	wantStatus(t, rr, http.StatusAccepted)
	var resp struct {
		CommandID int64 `json:"command_id"`
	}
	decodeJSON(t, rr, &resp)
	cmd, ok := srv.Hub.Command(resp.CommandID)
	if !ok || cmd.AgentID != localID {
		t.Fatalf("command agent = %+v, want local %d", cmd, localID)
	}
}

func TestSelfAgentExecutesRebuild(t *testing.T) {
	srv, _ := linodeServerFor(t, map[string][]linode.Instance{
		"pat1": {{ID: 7, Label: "web", Status: "running"}},
	})
	addAliceToken(t, srv, "main", "pat1")
	if err := srv.Store.SetRegCode("regcode-1"); err != nil {
		t.Fatalf("SetRegCode: %v", err)
	}

	// Inject a fast, fake rebuild into the self-agent's executor.
	var mu sync.Mutex
	status := map[int]string{7: "running"}
	base := linode.Instance{Region: "us-east", Type: "g6-standard-1", Image: "linode/debian12", IPv4: []string{"198.51.100.5"}}
	fake := &fakeAPI{}
	fake.getInstance = func(_ context.Context, id int) (linode.Instance, error) {
		mu.Lock()
		defer mu.Unlock()
		inst := base
		inst.ID = id
		inst.Status = status[id]
		if inst.Status == "" {
			inst.Status = "running"
		}
		return inst, nil
	}
	fake.shutdown = func(_ context.Context, id int) error {
		mu.Lock()
		defer mu.Unlock()
		status[id] = "offline"
		return nil
	}
	fake.boot = func(_ context.Context, id int) error {
		mu.Lock()
		defer mu.Unlock()
		status[id] = "running"
		return nil
	}
	fake.createInstance = func(_ context.Context, opts linode.CreateOptions) (linode.Instance, error) {
		mu.Lock()
		defer mu.Unlock()
		id := 99
		status[id] = "running"
		inst := base
		inst.ID = id
		inst.Status = "running"
		return inst, nil
	}
	fake.getIPs = func(_ context.Context, id int) (linode.InstanceIPs, error) {
		var ips linode.InstanceIPs
		ips.IPv4.Public = []linode.IPAddress{{Address: "203.0.113.7", Public: true}}
		return ips, nil
	}
	srv.newAgentExec = func() *agenthub.Executor {
		return &agenthub.Executor{
			SSHPort: 22,
			Run:     func(context.Context, string, int, string, string) error { return nil },
			Probe:   func(context.Context, string, int) error { return nil },
			NewAPI:  func(string) agenthub.LinodeAPI { return fake },
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	localID := srv.EnableSelfAgent(ctx, "本地面板")

	// Seed the panel-side config for the old instance; the agent reports the
	// replacement id, so the panel must re-point the stable cache row and the
	// new instance inherits the script.
	tok := srv.Store.TokensOf(aliceID)[0]
	if err := srv.Store.CacheInstances(tok.ID, []linode.Instance{{ID: 7, Label: "web"}}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Store.SetInstanceScript(7, "echo inherited"); err != nil {
		t.Fatal(err)
	}

	cmd, err := srv.Hub.Enqueue(localID, agenthub.Command{
		Type: agenthub.CmdReboot,
		Params: map[string]any{
			"id": 7, "script": "echo hi", "pat": "pat1",
		},
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	results := waitForResult(t, srv, 5*time.Second)
	if !resultOK(results, cmd.ID) {
		t.Fatalf("no ok rebuild result for %d: %+v", cmd.ID, results)
	}
	cached := srv.Store.CachedInstances(tok.ID)
	if len(cached) != 1 || cached[0].ID != 99 {
		t.Fatalf("cached instances = %+v, want the new instance 99", cached)
	}
	if script, ok := srv.Store.InstanceScript(99); !ok || script != "echo inherited" {
		t.Fatalf("InstanceScript(99) = %q, %v; want inherited script", script, ok)
	}
}
