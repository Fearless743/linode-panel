package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"linode-panel/internal/agentcrypto"
	"linode-panel/internal/agenthub"
	"linode-panel/internal/linode"
)

// agentTestServer builds a Server with a seeded store, alice (admin) logged
// in with one PAT, and a registration code set.
func agentTestServer(t *testing.T, regCode string) (*Server, *http.Cookie) {
	t.Helper()
	srv, cookie := linodeServerFor(t, map[string][]linode.Instance{
		"pat1": {{ID: 7, Label: "web", Status: "running", IPv4: []string{"10.0.0.1"}}},
	})
	addAliceToken(t, srv, "main", "pat1")
	if regCode != "" {
		if err := srv.Store.SetRegCode(regCode); err != nil {
			t.Fatalf("SetRegCode: %v", err)
		}
	}
	return srv, cookie
}

// registerAgent posts a registration request and returns the response body.
func registerAgent(t *testing.T, srv *Server, code, name string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"registration_code": code, "name": name})
	req := httptest.NewRequest(http.MethodPost, "/api/agents/register", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	return rr
}

func TestAgentRegisterOK(t *testing.T) {
	srv, _ := agentTestServer(t, "regcode-1")
	rr := registerAgent(t, srv, "regcode-1", "node-1")
	wantStatus(t, rr, http.StatusOK)
	var resp struct {
		AgentID int    `json:"agent_id"`
		Token   string `json:"agent_token"`
	}
	decodeJSON(t, rr, &resp)
	if resp.AgentID <= 0 || len(resp.Token) != 64 {
		t.Fatalf("register resp = %+v", resp)
	}
	if _, ok := srv.Hub.AgentByToken(resp.Token); !ok {
		t.Fatal("returned token does not authenticate")
	}
}

func TestAgentRegisterWrongCode(t *testing.T) {
	srv, _ := agentTestServer(t, "regcode-1")
	rr := registerAgent(t, srv, "wrong", "node-1")
	wantStatus(t, rr, http.StatusForbidden)
	var resp errorResponse
	decodeJSON(t, rr, &resp)
	if resp.Error != "注册码无效" {
		t.Fatalf("error = %q", resp.Error)
	}
}

func TestAgentRegisterNoCode(t *testing.T) {
	srv, _ := agentTestServer(t, "")
	rr := registerAgent(t, srv, "anything", "node-1")
	wantStatus(t, rr, http.StatusForbidden)
}

func TestAgentPollAndResult(t *testing.T) {
	srv, _ := agentTestServer(t, "regcode-1")
	rr := registerAgent(t, srv, "regcode-1", "node-1")
	var reg struct {
		AgentID int    `json:"agent_id"`
		Token   string `json:"agent_token"`
	}
	decodeJSON(t, rr, &reg)

	// Enqueue a command directly on the hub; poll must return it immediately.
	cmd, err := srv.Hub.Enqueue(reg.AgentID, agenthub.Command{
		Type:   agenthub.CmdPower,
		Params: map[string]any{"id": 7, "action": "boot"},
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	body, _ := json.Marshal(map[string]string{"hostname": "node-1"})
	req := httptest.NewRequest(http.MethodPost, "/api/agent/poll", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+reg.Token)
	rr2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr2, req)
	wantStatus(t, rr2, http.StatusOK)
	var poll struct {
		Commands []struct {
			ID   int64          `json:"id"`
			Type string         `json:"type"`
			Parm map[string]any `json:"params"`
		} `json:"commands"`
	}
	decodeJSON(t, rr2, &poll)
	if len(poll.Commands) != 1 || poll.Commands[0].ID != cmd.ID || poll.Commands[0].Type != agenthub.CmdPower {
		t.Fatalf("poll commands = %+v", poll.Commands)
	}
	if got, _ := srv.Hub.Agent(reg.AgentID); got.Hostname != "node-1" {
		t.Fatalf("hostname = %q, want node-1", got.Hostname)
	}

	// Report the result.
	resBody, _ := json.Marshal(map[string]any{"command_id": cmd.ID, "ok": true, "payload": map[string]any{"status": "running"}})
	req2 := httptest.NewRequest(http.MethodPost, "/api/agent/result", bytes.NewReader(resBody))
	req2.Header.Set("Authorization", "Bearer "+reg.Token)
	rr3 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr3, req2)
	wantStatus(t, rr3, http.StatusOK)
	results := srv.Hub.Results()
	if len(results) != 1 || results[0].CommandID != cmd.ID || !results[0].OK {
		t.Fatalf("results = %+v", results)
	}
}

func TestAgentPollUnauthorized(t *testing.T) {
	srv, _ := agentTestServer(t, "regcode-1")
	body, _ := json.Marshal(map[string]string{"hostname": "x"})
	req := httptest.NewRequest(http.MethodPost, "/api/agent/poll", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer not-a-token")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	wantStatus(t, rr, http.StatusUnauthorized)
}

func TestAgentResultWrongAgent(t *testing.T) {
	srv, _ := agentTestServer(t, "regcode-1")
	rr := registerAgent(t, srv, "regcode-1", "node-1")
	var reg struct {
		AgentID int    `json:"agent_id"`
		Token   string `json:"agent_token"`
	}
	decodeJSON(t, rr, &reg)
	cmd, _ := srv.Hub.Enqueue(reg.AgentID, agenthub.Command{Type: agenthub.CmdList, Params: map[string]any{}})
	srv.Hub.NextCommands(reg.AgentID, 0) // mark in_flight

	// A different (second) agent cannot complete the command.
	_, tok2 := srv.Hub.Register("node-2")
	resBody, _ := json.Marshal(map[string]any{"command_id": cmd.ID, "ok": true})
	req := httptest.NewRequest(http.MethodPost, "/api/agent/result", bytes.NewReader(resBody))
	req.Header.Set("Authorization", "Bearer "+tok2)
	rr2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr2, req)
	wantStatus(t, rr2, http.StatusNotFound)
}

func TestListAgentsPendingCount(t *testing.T) {
	srv, cookie := agentTestServer(t, "regcode-1")
	rr := registerAgent(t, srv, "regcode-1", "node-1")
	var reg struct {
		AgentID int    `json:"agent_id"`
		Token   string `json:"agent_token"`
	}
	decodeJSON(t, rr, &reg)
	srv.Hub.Enqueue(reg.AgentID, agenthub.Command{Type: agenthub.CmdList, Params: map[string]any{}})
	srv.Hub.Enqueue(reg.AgentID, agenthub.Command{Type: agenthub.CmdList, Params: map[string]any{}})

	rr2 := authedGet(t, srv, cookie, "/api/agents")
	wantStatus(t, rr2, http.StatusOK)
	var resp struct {
		Agents []struct {
			ID      int    `json:"id"`
			Name    string `json:"name"`
			Host    string `json:"hostname"`
			Last    string `json:"last_seen"`
			Pending int    `json:"pending"`
		} `json:"agents"`
	}
	decodeJSON(t, rr2, &resp)
	if len(resp.Agents) != 1 || resp.Agents[0].ID != reg.AgentID || resp.Agents[0].Pending != 2 {
		t.Fatalf("agents = %+v", resp.Agents)
	}
}

func TestAgentsAdminGate(t *testing.T) {
	srv, _ := agentTestServer(t, "regcode-1")
	// A view-only user must be denied.
	st := srv.Store
	if _, err := st.CreateUser("viewer", "viewer123", 2); err != nil { // group 2 = 只读
		t.Fatalf("CreateUser: %v", err)
	}
	cookie := loginAs(t, srv, "viewer", "viewer123")
	rr := authedGet(t, srv, cookie, "/api/agents")
	wantStatus(t, rr, http.StatusForbidden)
	rr2 := authedGet(t, srv, cookie, "/api/agent/commands")
	wantStatus(t, rr2, http.StatusForbidden)
}

func TestCreateAgentAndDelete(t *testing.T) {
	srv, cookie := agentTestServer(t, "regcode-1")
	rr := authedPost(t, srv, cookie, "/api/agents", `{"name":"manual-1"}`)
	wantStatus(t, rr, http.StatusOK)
	var resp struct {
		AgentID int    `json:"agent_id"`
		Token   string `json:"agent_token"`
		Code    string `json:"registration_code"`
	}
	decodeJSON(t, rr, &resp)
	if resp.AgentID <= 0 || resp.Token == "" || resp.Code != "regcode-1" {
		t.Fatalf("create agent resp = %+v", resp)
	}
	// Delete it.
	rr2 := authedDelete(t, srv, cookie, "/api/agents/"+strconv.Itoa(resp.AgentID))
	wantStatus(t, rr2, http.StatusOK)
	if _, ok := srv.Hub.Agent(resp.AgentID); ok {
		t.Fatal("agent still present after delete")
	}
	// Unknown id -> 404.
	rr3 := authedDelete(t, srv, cookie, "/api/agents/999")
	wantStatus(t, rr3, http.StatusNotFound)
}

func TestSetRegCode(t *testing.T) {
	srv, cookie := agentTestServer(t, "")
	// Explicit set.
	rr := authedPost(t, srv, cookie, "/api/agents/regcode", `{"code":"my-code"}`)
	wantStatus(t, rr, http.StatusOK)
	if got := srv.Store.RegCode(); got != "my-code" {
		t.Fatalf("RegCode = %q, want my-code", got)
	}
	// Empty body -> generate random 16B hex.
	rr2 := authedPost(t, srv, cookie, "/api/agents/regcode", "")
	wantStatus(t, rr2, http.StatusOK)
	var resp struct {
		Code string `json:"registration_code"`
	}
	decodeJSON(t, rr2, &resp)
	if len(resp.Code) != 32 {
		t.Fatalf("generated code = %q, want 32 hex chars", resp.Code)
	}
	if resp.Code == "my-code" {
		t.Fatal("generated code equals old code")
	}
}

func TestAgentCommandsLimit(t *testing.T) {
	srv, cookie := agentTestServer(t, "regcode-1")
	rr := registerAgent(t, srv, "regcode-1", "node-1")
	var reg struct {
		AgentID int    `json:"agent_id"`
		Token   string `json:"agent_token"`
	}
	decodeJSON(t, rr, &reg)
	for range 3 {
		srv.Hub.Enqueue(reg.AgentID, agenthub.Command{Type: agenthub.CmdList, Params: map[string]any{}})
	}
	rr2 := authedGet(t, srv, cookie, "/api/agent/commands?limit=2")
	wantStatus(t, rr2, http.StatusOK)
	var resp struct {
		Commands []agenthub.Command `json:"commands"`
		Results  []agenthub.Result  `json:"results"`
	}
	decodeJSON(t, rr2, &resp)
	if len(resp.Commands) != 2 {
		t.Fatalf("commands len = %d, want 2", len(resp.Commands))
	}
}

func TestViaAgentPowerQueues(t *testing.T) {
	srv, cookie := agentTestServer(t, "regcode-1")
	rr := registerAgent(t, srv, "regcode-1", "node-1")
	var reg struct {
		AgentID int    `json:"agent_id"`
		Token   string `json:"agent_token"`
	}
	decodeJSON(t, rr, &reg)

	rr2 := authedPost(t, srv, cookie, "/api/instances/7/boot?via_agent=1", "")
	wantStatus(t, rr2, http.StatusAccepted)
	var resp struct {
		CommandID int64 `json:"command_id"`
	}
	decodeJSON(t, rr2, &resp)
	cmd, ok := srv.Hub.Command(resp.CommandID)
	if !ok {
		t.Fatalf("command %d not found", resp.CommandID)
	}
	if cmd.Type != agenthub.CmdPower || cmd.Params["action"] != "boot" {
		t.Fatalf("cmd = %+v", cmd)
	}
	if id, _ := cmd.Params["id"].(int); id != 7 {
		t.Fatalf("cmd id param = %#v, want 7", cmd.Params["id"])
	}
	sealed, _ := cmd.Params["sealed_pat"].(string)
	plain, err := agentcrypto.Open(agentcrypto.DeriveKey("regcode-1"), sealed)
	if err != nil {
		t.Fatalf("Open sealed pat: %v", err)
	}
	if string(plain) != "pat1" {
		t.Fatalf("sealed PAT = %q, want pat1", string(plain))
	}
}

func TestViaAgentRebuildQueues(t *testing.T) {
	srv, cookie := agentTestServer(t, "regcode-1")
	rr := registerAgent(t, srv, "regcode-1", "node-1")
	var reg struct {
		AgentID int    `json:"agent_id"`
		Token   string `json:"agent_token"`
	}
	decodeJSON(t, rr, &reg)

	body := `{"script":"echo boot","reserved_ip":"203.0.113.9"}`
	rr2 := authedPost(t, srv, cookie, "/api/instances/7/rebuild?via_agent=1", body)
	wantStatus(t, rr2, http.StatusAccepted)
	var resp struct {
		CommandID int64 `json:"command_id"`
	}
	decodeJSON(t, rr2, &resp)
	cmd, ok := srv.Hub.Command(resp.CommandID)
	if !ok {
		t.Fatalf("command %d not found", resp.CommandID)
	}
	if cmd.Type != agenthub.CmdReboot ||
		cmd.Params["script"] != "echo boot" || cmd.Params["reserved_ip"] != "203.0.113.9" {
		t.Fatalf("cmd = %+v", cmd)
	}
	if id, _ := cmd.Params["id"].(int); id != 7 {
		t.Fatalf("cmd id param = %#v, want 7", cmd.Params["id"])
	}
	sealed, _ := cmd.Params["sealed_pat"].(string)
	plain, err := agentcrypto.Open(agentcrypto.DeriveKey("regcode-1"), sealed)
	if err != nil || string(plain) != "pat1" {
		t.Fatalf("sealed PAT = %q, err=%v", string(plain), err)
	}
}

func TestViaAgentNoRegCode(t *testing.T) {
	srv, cookie := agentTestServer(t, "")
	// Register an agent anyway (hub has one) but no reg code.
	_, _ = srv.Hub.Register("orphan")
	rr := authedPost(t, srv, cookie, "/api/instances/7/boot?via_agent=1", "")
	wantStatus(t, rr, http.StatusBadRequest)
	var resp errorResponse
	decodeJSON(t, rr, &resp)
	if !strings.Contains(resp.Error, "没有已注册的 agent") {
		t.Fatalf("error = %q", resp.Error)
	}
}

func TestViaAgentNoAgent(t *testing.T) {
	srv, cookie := agentTestServer(t, "regcode-1")
	rr := authedPost(t, srv, cookie, "/api/instances/7/boot?via_agent=1", "")
	wantStatus(t, rr, http.StatusBadRequest)
}

func TestDirectPathRegression(t *testing.T) {
	srv, cookie := linodeServerFor(t, map[string][]linode.Instance{
		"pat1": {{ID: 7, Label: "web", Status: "running", IPv4: []string{"10.0.0.1"}}},
	})
	addAliceToken(t, srv, "main", "pat1")
	// No via_agent: direct call hits the fake Linode server.
	rr := authedPost(t, srv, cookie, "/api/instances/7/boot", "")
	wantStatus(t, rr, http.StatusOK)
	var resp struct {
		OK bool `json:"ok"`
	}
	decodeJSON(t, rr, &resp)
	if !resp.OK {
		t.Fatal("direct boot ok = false")
	}
}

// registerAgentID registers an agent and returns its id.
func registerAgentID(t *testing.T, srv *Server, code, name string) int {
	t.Helper()
	rr := registerAgent(t, srv, code, name)
	var reg struct {
		AgentID int `json:"agent_id"`
	}
	decodeJSON(t, rr, &reg)
	return reg.AgentID
}

// tokenIDOf returns the first token id of the calling user (alice).
func tokenIDOf(t *testing.T, srv *Server, cookie *http.Cookie) int {
	t.Helper()
	rr := authedGet(t, srv, cookie, "/api/account/tokens")
	wantStatus(t, rr, http.StatusOK)
	var list struct {
		Tokens []struct {
			ID int `json:"id"`
		} `json:"tokens"`
	}
	decodeJSON(t, rr, &list)
	if len(list.Tokens) == 0 {
		t.Fatal("no tokens")
	}
	return list.Tokens[0].ID
}

func TestAccountAgentsList(t *testing.T) {
	srv, cookie := agentTestServer(t, "regcode-1")
	agent1 := registerAgentID(t, srv, "regcode-1", "node-1")
	agent2 := registerAgentID(t, srv, "regcode-1", "node-2")

	rr := authedGet(t, srv, cookie, "/api/account/agents")
	wantStatus(t, rr, http.StatusOK)
	var resp struct {
		Agents []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"agents"`
	}
	decodeJSON(t, rr, &resp)
	if len(resp.Agents) != 2 {
		t.Fatalf("agents = %+v", resp.Agents)
	}
	if resp.Agents[0].ID != agent1 || resp.Agents[1].ID != agent2 {
		t.Fatalf("agents = %+v, want [%d %d]", resp.Agents, agent1, agent2)
	}
	if resp.Agents[0].Name != "node-1" {
		t.Fatalf("agent name = %q, want node-1", resp.Agents[0].Name)
	}
}

func TestAccountAgentsAvailableToNonAdmin(t *testing.T) {
	srv, _ := agentTestServer(t, "regcode-1")
	if _, err := srv.Store.CreateUser("viewer", "viewer123", 2); err != nil { // group 2 = 只读
		t.Fatalf("CreateUser: %v", err)
	}
	cookie := loginAs(t, srv, "viewer", "viewer123")
	// /api/agents requires admin, but /api/account/agents must not.
	rr := authedGet(t, srv, cookie, "/api/agents")
	wantStatus(t, rr, http.StatusForbidden)
	rr2 := authedGet(t, srv, cookie, "/api/account/agents")
	wantStatus(t, rr2, http.StatusOK)
}

func TestSetTokenAgentHTTP(t *testing.T) {
	srv, cookie := agentTestServer(t, "regcode-1")
	agent1 := registerAgentID(t, srv, "regcode-1", "node-1")
	tid := tokenIDOf(t, srv, cookie)

	// Bind to agent1.
	rr := authedPut(t, srv, cookie, "/api/account/tokens/"+strconv.Itoa(tid)+"/agent",
		`{"agent_id":`+strconv.Itoa(agent1)+`}`)
	wantStatus(t, rr, http.StatusOK)
	list := authedGet(t, srv, cookie, "/api/account/tokens")
	wantStatus(t, list, http.StatusOK)
	var tokens struct {
		Tokens []struct {
			AgentID   int    `json:"agent_id"`
			AgentName string `json:"agent_name"`
		} `json:"tokens"`
	}
	decodeJSON(t, list, &tokens)
	if tokens.Tokens[0].AgentID != agent1 || tokens.Tokens[0].AgentName != "node-1" {
		t.Fatalf("after bind = %+v", tokens.Tokens[0])
	}

	// Unbind.
	rr2 := authedPut(t, srv, cookie, "/api/account/tokens/"+strconv.Itoa(tid)+"/agent", `{"agent_id":0}`)
	wantStatus(t, rr2, http.StatusOK)
	list2 := authedGet(t, srv, cookie, "/api/account/tokens")
	var tokens2 struct {
		Tokens []struct {
			AgentID   int    `json:"agent_id"`
			AgentName string `json:"agent_name"`
		} `json:"tokens"`
	}
	decodeJSON(t, list2, &tokens2)
	if tokens2.Tokens[0].AgentID != 0 || tokens2.Tokens[0].AgentName != "" {
		t.Fatalf("after unbind = %+v", tokens2.Tokens[0])
	}

	// Unknown agent -> 400.
	rr3 := authedPut(t, srv, cookie, "/api/account/tokens/"+strconv.Itoa(tid)+"/agent", `{"agent_id":999}`)
	wantStatus(t, rr3, http.StatusBadRequest)
	// Unknown token -> 400.
	rr4 := authedPut(t, srv, cookie, "/api/account/tokens/999/agent", `{"agent_id":0}`)
	wantStatus(t, rr4, http.StatusBadRequest)
}

func TestViaAgentRoutesToBoundAgent(t *testing.T) {
	srv, cookie := agentTestServer(t, "regcode-1")
	agent1 := registerAgentID(t, srv, "regcode-1", "node-1")
	agent2 := registerAgentID(t, srv, "regcode-1", "node-2")
	tid := tokenIDOf(t, srv, cookie)

	// Unbound account falls back to the first registered agent (agent1).
	rr := authedPost(t, srv, cookie, "/api/instances/7/boot?via_agent=1", "")
	wantStatus(t, rr, http.StatusAccepted)
	var resp struct {
		CommandID int64 `json:"command_id"`
	}
	decodeJSON(t, rr, &resp)
	if cmd, ok := srv.Hub.Command(resp.CommandID); !ok || cmd.AgentID != agent1 {
		t.Fatalf("unbound command agent = %+v, want agent %d", cmd, agent1)
	}

	// Bind the account to agent2; the next command must go there.
	if r := authedPut(t, srv, cookie, "/api/account/tokens/"+strconv.Itoa(tid)+"/agent",
		`{"agent_id":`+strconv.Itoa(agent2)+`}`); r.Code != http.StatusOK {
		t.Fatalf("bind status = %d", r.Code)
	}
	rr2 := authedPost(t, srv, cookie, "/api/instances/7/boot?via_agent=1", "")
	wantStatus(t, rr2, http.StatusAccepted)
	decodeJSON(t, rr2, &resp)
	if cmd, ok := srv.Hub.Command(resp.CommandID); !ok || cmd.AgentID != agent2 {
		t.Fatalf("bound command agent = %+v, want agent %d", cmd, agent2)
	}

	// An explicit ?agent_id=N override beats the account binding.
	rr3 := authedPost(t, srv, cookie,
		"/api/instances/7/boot?via_agent=1&agent_id="+strconv.Itoa(agent1), "")
	wantStatus(t, rr3, http.StatusAccepted)
	decodeJSON(t, rr3, &resp)
	if cmd, ok := srv.Hub.Command(resp.CommandID); !ok || cmd.AgentID != agent1 {
		t.Fatalf("override command agent = %+v, want agent %d", cmd, agent1)
	}

	// Unbind falls back to the first registered agent again.
	if r := authedPut(t, srv, cookie, "/api/account/tokens/"+strconv.Itoa(tid)+"/agent",
		`{"agent_id":0}`); r.Code != http.StatusOK {
		t.Fatalf("unbind status = %d", r.Code)
	}
	rr4 := authedPost(t, srv, cookie, "/api/instances/7/boot?via_agent=1", "")
	wantStatus(t, rr4, http.StatusAccepted)
	decodeJSON(t, rr4, &resp)
	if cmd, ok := srv.Hub.Command(resp.CommandID); !ok || cmd.AgentID != agent1 {
		t.Fatalf("after unbind command agent = %+v, want agent %d", cmd, agent1)
	}
}

// TestAgentPollLongPollWakes verifies the poll blocks when no command is
// queued and wakes immediately when one is enqueued.
func TestAgentPollLongPollWakes(t *testing.T) {
	srv, _ := agentTestServer(t, "regcode-1")
	rr := registerAgent(t, srv, "regcode-1", "node-1")
	var reg struct {
		AgentID int    `json:"agent_id"`
		Token   string `json:"agent_token"`
	}
	decodeJSON(t, rr, &reg)

	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()

	body, _ := json.Marshal(map[string]string{"hostname": "node-1"})
	type outcome struct {
		status int
		n      int
		err    error
	}
	out := make(chan outcome, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodPost, httpSrv.URL+"/api/agent/poll", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+reg.Token)
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			out <- outcome{err: err}
			return
		}
		defer resp.Body.Close()
		var parsed struct {
			Commands []struct{} `json:"commands"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&parsed)
		out <- outcome{status: resp.StatusCode, n: len(parsed.Commands)}
	}()

	// Wait until the poll is blocked (it will hold the connection), then
	// enqueue a command to wake it.
	time.Sleep(300 * time.Millisecond)
	if _, err := srv.Hub.Enqueue(reg.AgentID, agenthub.Command{Type: agenthub.CmdList, Params: map[string]any{}}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	select {
	case o := <-out:
		if o.err != nil {
			t.Fatalf("poll request: %v", o.err)
		}
		if o.status != http.StatusOK || o.n != 1 {
			t.Fatalf("poll outcome = %+v, want 200 with 1 command", o)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("poll did not wake after Enqueue within 6s")
	}
}
