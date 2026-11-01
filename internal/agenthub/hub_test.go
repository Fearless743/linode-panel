package agenthub

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestRegisterAndToken(t *testing.T) {
	h := NewHub()
	agent, token := h.Register("agent-1")

	if agent.ID != 1 {
		t.Fatalf("ID = %d, want 1", agent.ID)
	}
	if agent.Name != "agent-1" {
		t.Fatalf("Name = %q, want agent-1", agent.Name)
	}
	if len(token) != 64 { // 32 bytes hex
		t.Fatalf("token length = %d, want 64", len(token))
	}
	// TokenHash is the stored sha256 hex (never serialized); it must be
	// non-empty so the record is complete.
	if agent.TokenHash == "" {
		t.Fatal("Register returned empty TokenHash")
	}

	// Token validates.
	got, ok := h.AgentByToken(token)
	if !ok {
		t.Fatal("AgentByToken failed for valid token")
	}
	if got.ID != agent.ID {
		t.Fatalf("AgentByToken ID = %d, want %d", got.ID, agent.ID)
	}

	// Wrong token fails.
	if _, ok := h.AgentByToken(token + "00"); ok {
		t.Fatal("AgentByToken accepted wrong token")
	}
	if _, ok := h.AgentByToken(""); ok {
		t.Fatal("AgentByToken accepted empty token")
	}
}

func TestRegisterGeneratesUniqueTokens(t *testing.T) {
	h := NewHub()
	seen := make(map[string]bool)
	for range 100 {
		_, tok := h.Register("a")
		if seen[tok] {
			t.Fatalf("duplicate token %q", tok)
		}
		seen[tok] = true
	}
}

func TestAgentByIDAndList(t *testing.T) {
	h := NewHub()
	a1, _ := h.Register("one")
	a2, _ := h.Register("two")

	if _, ok := h.Agent(99); ok {
		t.Fatal("Agent(99) returned true for unknown ID")
	}
	got, ok := h.Agent(a1.ID)
	if !ok || got.Name != "one" {
		t.Fatalf("Agent(%d) = %+v,%v", a1.ID, got, ok)
	}

	list := h.Agents()
	if len(list) != 2 {
		t.Fatalf("Agents() len = %d, want 2", len(list))
	}
	if list[0].ID != a1.ID || list[1].ID != a2.ID {
		t.Fatalf("Agents() order = [%d,%d], want [%d,%d]", list[0].ID, list[1].ID, a1.ID, a2.ID)
	}
}

func TestEnqueueUnknownAgent(t *testing.T) {
	h := NewHub()
	_, err := h.Enqueue(42, Command{Type: CmdList})
	if !errors.Is(err, ErrNoAgent) {
		t.Fatalf("Enqueue err = %v, want ErrNoAgent", err)
	}
}

func TestEnqueueAssignsFields(t *testing.T) {
	h := NewHub()
	agent, _ := h.Register("a")

	cmd, err := h.Enqueue(agent.ID, Command{Type: CmdList, Params: map[string]any{}})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if cmd.ID != 1 {
		t.Fatalf("cmd.ID = %d, want 1", cmd.ID)
	}
	if cmd.AgentID != agent.ID {
		t.Fatalf("cmd.AgentID = %d, want %d", cmd.AgentID, agent.ID)
	}
	if cmd.Status != StatusQueued {
		t.Fatalf("cmd.Status = %q, want queued", cmd.Status)
	}
	if cmd.CreatedAt == "" {
		t.Fatal("cmd.CreatedAt empty")
	}
}

func TestNextCommandsFIFO(t *testing.T) {
	h := NewHub()
	agent, _ := h.Register("a")

	var c1, c2, c3 Command
	var err error
	if c1, err = h.Enqueue(agent.ID, Command{Type: CmdList}); err != nil {
		t.Fatal(err)
	}
	if c2, err = h.Enqueue(agent.ID, Command{Type: CmdPower, Params: map[string]any{"action": ActionBoot}}); err != nil {
		t.Fatal(err)
	}
	if c3, err = h.Enqueue(agent.ID, Command{Type: CmdReboot}); err != nil {
		t.Fatal(err)
	}

	cmds := h.NextCommands(context.Background(), agent.ID, 0)
	if len(cmds) != 3 {
		t.Fatalf("NextCommands len = %d, want 3", len(cmds))
	}
	if cmds[0].ID != c1.ID || cmds[1].ID != c2.ID || cmds[2].ID != c3.ID {
		t.Fatalf("FIFO order = [%d,%d,%d], want [%d,%d,%d]",
			cmds[0].ID, cmds[1].ID, cmds[2].ID, c1.ID, c2.ID, c3.ID)
	}
	if cmds[0].Status != StatusInFlight {
		t.Fatalf("status after dequeue = %q, want in_flight", cmds[0].Status)
	}
	// Second call drains nothing.
	if got := h.NextCommands(context.Background(), agent.ID, 0); len(got) != 0 {
		t.Fatalf("second NextCommands len = %d, want 0", len(got))
	}
}

func TestNextCommandsUnknownAgentReturnsEmpty(t *testing.T) {
	h := NewHub()
	cmds := h.NextCommands(context.Background(), 99, 0)
	if cmds == nil {
		t.Fatal("NextCommands returned nil for unknown agent, want non-nil empty")
	}
	if len(cmds) != 0 {
		t.Fatalf("len = %d, want 0", len(cmds))
	}
}

func TestNextCommandsTimeoutReturnsEmpty(t *testing.T) {
	h := NewHub()
	agent, _ := h.Register("a")
	start := time.Now()
	cmds := h.NextCommands(context.Background(), agent.ID, 30*time.Millisecond)
	if time.Since(start) < 25*time.Millisecond {
		t.Fatalf("returned before wait elapsed: %v", time.Since(start))
	}
	if cmds == nil {
		t.Fatal("returned nil on timeout, want non-nil empty")
	}
	if len(cmds) != 0 {
		t.Fatalf("len = %d, want 0", len(cmds))
	}
}

func TestNextCommandsWakeOnEnqueue(t *testing.T) {
	h := NewHub()
	agent, _ := h.Register("a")

	const delay = 50 * time.Millisecond
	var wg sync.WaitGroup
	var got []Command
	wg.Add(1)
	go func() {
		defer wg.Done()
		got = h.NextCommands(context.Background(), agent.ID, 5*time.Second)
	}()

	time.Sleep(delay)
	cmd, err := h.Enqueue(agent.ID, Command{Type: CmdList})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("NextCommands was not woken by Enqueue")
	}

	if len(got) != 1 || got[0].ID != cmd.ID {
		t.Fatalf("woken commands = %+v, want [%d]", got, cmd.ID)
	}
}

func TestNextCommandsUpdatesLastSeen(t *testing.T) {
	h := NewHub()
	agent, _ := h.Register("a")
	h.NextCommands(context.Background(), agent.ID, 0)
	updated, ok := h.Agent(agent.ID)
	if !ok || updated.LastSeen == "" {
		t.Fatalf("LastSeen not updated: %+v", updated)
	}
}

func TestCompleteCommand(t *testing.T) {
	h := NewHub()
	agent, _ := h.Register("a")
	cmd, _ := h.Enqueue(agent.ID, Command{Type: CmdList})
	h.NextCommands(context.Background(), agent.ID, 0)

	if h.CompleteCommand(999, Result{OK: true}) {
		t.Fatal("CompleteCommand(999) returned true for unknown ID")
	}
	res := Result{OK: true, Payload: map[string]any{"x": 1}}
	if !h.CompleteCommand(cmd.ID, res) {
		t.Fatal("CompleteCommand returned false for known ID")
	}

	// Command no longer pending.
	pending := h.PendingCommands()
	if len(pending) != 0 {
		t.Fatalf("PendingCommands after complete = %+v, want empty", pending)
	}

	// Result recorded, newest first.
	results := h.Results()
	if len(results) != 1 {
		t.Fatalf("Results len = %d, want 1", len(results))
	}
	r := results[0]
	if r.CommandID != cmd.ID || r.AgentID != agent.ID || !r.OK || r.At == "" {
		t.Fatalf("result = %+v", r)
	}
}

func TestResultsNewestFirst(t *testing.T) {
	h := NewHub()
	agent, _ := h.Register("a")

	ids := make([]int64, 0, 3)
	for range 3 {
		c, _ := h.Enqueue(agent.ID, Command{Type: CmdList})
		ids = append(ids, c.ID)
	}
	h.NextCommands(context.Background(), agent.ID, 0)
	for _, id := range ids {
		if !h.CompleteCommand(id, Result{OK: true}) {
			t.Fatalf("CompleteCommand(%d) failed", id)
		}
	}

	results := h.Results()
	if len(results) != 3 {
		t.Fatalf("Results len = %d, want 3", len(results))
	}
	for i, want := range []int64{ids[2], ids[1], ids[0]} {
		if results[i].CommandID != want {
			t.Fatalf("Results[%d].CommandID = %d, want %d (newest first)", i, results[i].CommandID, want)
		}
	}
}

func TestPendingCommandsOldestFirst(t *testing.T) {
	h := NewHub()
	agent, _ := h.Register("a")

	var ids []int64
	for range 3 {
		c, _ := h.Enqueue(agent.ID, Command{Type: CmdList})
		ids = append(ids, c.ID)
	}

	// Before dequeue: all commands are queued.
	pending := h.PendingCommands()
	if len(pending) != 3 {
		t.Fatalf("PendingCommands len = %d, want 3", len(pending))
	}
	for i, want := range ids {
		if pending[i].ID != want {
			t.Fatalf("PendingCommands[%d].ID = %d, want %d (oldest first)", i, pending[i].ID, want)
		}
		if pending[i].Status != StatusQueued {
			t.Fatalf("pending[%d].Status = %q, want queued", i, pending[i].Status)
		}
	}

	// Dequeue all: every command becomes in_flight, still oldest first.
	h.NextCommands(context.Background(), agent.ID, 0)
	pending = h.PendingCommands()
	if len(pending) != 3 {
		t.Fatalf("PendingCommands after dequeue len = %d, want 3", len(pending))
	}
	for i, want := range ids {
		if pending[i].ID != want {
			t.Fatalf("PendingCommands[%d].ID = %d, want %d (oldest first)", i, pending[i].ID, want)
		}
		if pending[i].Status != StatusInFlight {
			t.Fatalf("pending[%d].Status = %q, want in_flight", i, pending[i].Status)
		}
	}
}

func TestConcurrentUse(t *testing.T) {
	h := NewHub()
	const n = 8
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			agent, token := h.Register("concurrent")
			if _, ok := h.AgentByToken(token); !ok {
				t.Errorf("token not found for agent %d", agent.ID)
			}
			cmd, err := h.Enqueue(agent.ID, Command{Type: CmdList})
			if err != nil {
				t.Errorf("Enqueue: %v", err)
				return
			}
			got := h.NextCommands(context.Background(), agent.ID, 0)
			for _, c := range got {
				if !h.CompleteCommand(c.ID, Result{OK: true, Error: ""}) {
					t.Errorf("CompleteCommand(%d) failed", c.ID)
				}
			}
			_ = cmd
			h.Results()
			h.PendingCommands()
			h.Agents()
			h.Agent(agent.ID)
		}()
	}
	wg.Wait()

	if got := len(h.Agents()); got != n {
		t.Fatalf("registered agents = %d, want %d", got, n)
	}
	if got := len(h.Results()); got != n {
		t.Fatalf("results = %d, want %d", got, n)
	}
}

func TestConcurrentPollAndEnqueue(t *testing.T) {
	h := NewHub()
	agent, _ := h.Register("poll")

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				// Drain any commands still queued so the result count is
				// exact even if the poller was idle when done closed.
				for _, c := range h.NextCommands(context.Background(), agent.ID, 0) {
					h.CompleteCommand(c.ID, Result{OK: true})
				}
				return
			default:
			}
			cmds := h.NextCommands(context.Background(), agent.ID, 5*time.Millisecond)
			for _, c := range cmds {
				h.CompleteCommand(c.ID, Result{OK: true})
			}
		}
	}()

	for range 100 {
		if _, err := h.Enqueue(agent.ID, Command{Type: CmdList}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}
	close(done)
	wg.Wait()

	if got := len(h.Results()); got != 100 {
		t.Fatalf("results = %d, want 100", got)
	}
}

func TestDeleteAgent(t *testing.T) {
	h := NewHub()
	a, _ := h.Register("one")
	_, err := h.Enqueue(a.ID, Command{Type: CmdList})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if !h.Delete(a.ID) {
		t.Fatal("Delete returned false for known agent")
	}
	if h.Delete(a.ID) {
		t.Fatal("Delete returned true for already-deleted agent")
	}
	if _, ok := h.Agent(a.ID); ok {
		t.Fatal("agent still present after Delete")
	}
	// Queued command for the deleted agent is gone; pending count is zero.
	if n := h.PendingFor(a.ID); n != 0 {
		t.Fatalf("PendingFor(deleted) = %d, want 0", n)
	}
}

func TestCommandByID(t *testing.T) {
	h := NewHub()
	a, _ := h.Register("a")
	cmd, err := h.Enqueue(a.ID, Command{Type: CmdPower, Params: map[string]any{"action": ActionBoot}})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	got, ok := h.Command(cmd.ID)
	if !ok || got.ID != cmd.ID || got.Type != CmdPower || got.Status != StatusQueued {
		t.Fatalf("Command = %+v ok=%v", got, ok)
	}
	if _, ok := h.Command(cmd.ID + 1); ok {
		t.Fatal("Command returned true for unknown id")
	}
}

func TestPendingFor(t *testing.T) {
	h := NewHub()
	a, _ := h.Register("a")
	if n := h.PendingFor(a.ID); n != 0 {
		t.Fatalf("PendingFor = %d, want 0", n)
	}
	for range 3 {
		if _, err := h.Enqueue(a.ID, Command{Type: CmdList}); err != nil {
			t.Fatal(err)
		}
	}
	if n := h.PendingFor(a.ID); n != 3 {
		t.Fatalf("PendingFor = %d, want 3", n)
	}
	// In-flight commands still count as pending.
	h.NextCommands(context.Background(), a.ID, 0)
	if n := h.PendingFor(a.ID); n != 3 {
		t.Fatalf("PendingFor after dequeue = %d, want 3", n)
	}
}

func TestNextCommandsContextCancel(t *testing.T) {
	h := NewHub()
	a, _ := h.Register("a")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if cmds := h.NextCommands(ctx, a.ID, 10*time.Second); len(cmds) != 0 {
		t.Fatalf("expected empty on context timeout, got %d", len(cmds))
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("NextCommands blocked too long: %v", elapsed)
	}
}

func TestSetHostname(t *testing.T) {
	h := NewHub()
	a, _ := h.Register("one")
	h.SetHostname(a.ID, "host-1")
	if got, ok := h.Agent(a.ID); !ok || got.Hostname != "host-1" {
		t.Fatalf("Agent hostname = %+v ok=%v", got, ok)
	}
	// Unknown id is a no-op.
	h.SetHostname(999, "nope")
	if _, ok := h.Agent(999); ok {
		t.Fatal("SetHostname created an unknown agent")
	}
}

func TestRename(t *testing.T) {
	h := NewHub()
	a, _ := h.Register("old")
	if !h.Rename(a.ID, "new") {
		t.Fatal("Rename returned false for a known agent")
	}
	if got, ok := h.Agent(a.ID); !ok || got.Name != "new" {
		t.Fatalf("Agent name = %+v ok=%v", got, ok)
	}
	// Unknown id returns false.
	if h.Rename(999, "x") {
		t.Fatal("Rename returned true for an unknown agent")
	}
}
