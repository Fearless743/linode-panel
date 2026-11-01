package api

import (
	"context"
	"log/slog"
	"os"
	"time"

	"linode-panel/internal/agenthub"
)

// localAgent runs agent commands in-process on the panel host, so the panel
// itself can act as an agent. It is registered in the Server's Hub and pulls
// queued commands the same way a remote agent would, but executes them with
// the shared agenthub Executor instead of speaking HTTP.
type localAgent struct {
	s       *Server
	agentID int
	exec    *agenthub.Executor
	log     *slog.Logger
}

// EnableSelfAgent registers the panel itself as an agent in its own Hub and
// starts an in-process command loop, returning the registered agent ID. The
// loop stops when ctx is cancelled. The agent name defaults to "本地面板".
func (s *Server) EnableSelfAgent(ctx context.Context, name string) int {
	if name == "" {
		name = "本地面板"
	}
	agent, _ := s.Hub.Register(name)
	s.localAgentID = agent.ID
	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		s.Hub.SetHostname(agent.ID, hostname)
	}
	la := &localAgent{
		s:       s,
		agentID: agent.ID,
		exec:    s.newLocalAgentExecutor(),
		log:     slog.Default(),
	}
	go la.run(ctx)
	return agent.ID
}

// newLocalAgentExecutor builds the shared Executor for the self-agent. It
// reuses the server's per-PAT client cache (so APIBase applies) and the
// server's SSH port. Tests may override newAgentExec.
func (s *Server) newLocalAgentExecutor() *agenthub.Executor {
	if s.newAgentExec != nil {
		return s.newAgentExec()
	}
	ex := agenthub.NewExecutor()
	ex.APIBase = s.APIBase
	ex.SSHPort = s.sshPort()
	ex.NewAPI = func(pat string) agenthub.LinodeAPI {
		return s.clientFor(pat)
	}
	ex.Log = slog.Default()
	return ex
}

// run is the in-process equivalent of the remote agent's poll loop.
func (l *localAgent) run(ctx context.Context) {
	for {
		cmds := l.s.Hub.NextCommands(ctx, l.agentID, 25*time.Second)
		if ctx.Err() != nil {
			return
		}
		for _, cmd := range cmds {
			l.execute(ctx, cmd)
		}
	}
}

// execute runs one command and reports its result (and progress) via the Hub.
func (l *localAgent) execute(ctx context.Context, cmd agenthub.Command) {
	l.log.Info("本地 agent 执行命令", "command_id", cmd.ID, "type", cmd.Type)
	l.exec.Execute(ctx, cmd, func(ok bool, errMsg string, payload map[string]any) {
		l.s.completeAgentCommand(cmd, agenthub.Result{OK: ok, Error: errMsg, Payload: payload})
	})
}
