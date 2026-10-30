package api

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"os"
	"time"

	"linode-panel/internal/agentcrypto"
	"linode-panel/internal/agenthub"
)

// localAgent runs agent commands in-process on the panel host, so the panel
// itself can act as an agent. It is registered in the Server's Hub and pulls
// queued commands the same way a remote agent would, but executes them using
// the panel's own Linode client and rebuild Manager instead of speaking HTTP.
type localAgent struct {
	s       *Server
	agentID int
	key     []byte // AES key derived from the registration code
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
	s.localAgentKey = localAgentKeyFor(s.Store.RegCode())
	la := &localAgent{
		s:       s,
		agentID: agent.ID,
		key:     s.localAgentKey,
		log:     slog.Default(),
	}
	go la.run(ctx)
	return agent.ID
}

// localAgentKeyFor returns the key used to seal commands routed to the panel's
// own agent. With a registration code set it matches the remote agent key
// derivation; without one it falls back to a random per-run key, so the panel
// can act as an agent out of the box (no registration code required).
func localAgentKeyFor(regCode string) []byte {
	if regCode != "" {
		return agentcrypto.DeriveKey(regCode)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return agentcrypto.DeriveKey(regCode)
	}
	return key
}

// run is the in-process equivalent of the remote agent's poll loop.
func (l *localAgent) run(ctx context.Context) {
	for {
		cmds := l.s.Hub.NextCommandsContext(ctx, l.agentID, 25*time.Second)
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
	report := func(ok bool, errMsg string, payload map[string]any) {
		l.s.Hub.CompleteCommand(cmd.ID, agenthub.Result{OK: ok, Error: errMsg, Payload: payload})
	}
	switch cmd.Type {
	case agenthub.CmdPower:
		ok, errMsg := l.power(ctx, cmd)
		report(ok, errMsg, map[string]any{})
	case agenthub.CmdReboot:
		l.rebuild(ctx, cmd, report)
	default:
		report(false, "本地 agent 不支持命令类型: "+cmd.Type, nil)
	}
}

// openPAT decrypts the command's sealed_pat with the registration-code key.
func (l *localAgent) openPAT(cmd agenthub.Command) (string, error) {
	sealed, _ := cmd.Params["sealed_pat"].(string)
	if sealed == "" {
		return "", errors.New("命令未携带有效的 sealed_pat")
	}
	plain, err := agentcrypto.Open(l.key, sealed)
	if err != nil {
		return "", errors.New("解密 PAT 失败: " + err.Error())
	}
	return string(plain), nil
}

// power performs a boot/shutdown/reboot with the command's PAT.
func (l *localAgent) power(ctx context.Context, cmd agenthub.Command) (bool, string) {
	pat, err := l.openPAT(cmd)
	if err != nil {
		return false, err.Error()
	}
	action, _ := cmd.Params["action"].(string)
	id, ok := agentIntParam(cmd.Params["id"])
	if !ok || id <= 0 {
		return false, "无效的实例 ID"
	}
	client := l.s.clientFor(pat)
	var err2 error
	switch action {
	case "boot":
		err2 = client.Boot(ctx, id)
	case "shutdown":
		err2 = client.Shutdown(ctx, id)
	case "reboot":
		err2 = client.Reboot(ctx, id)
	default:
		return false, "未知电源操作: " + action
	}
	if err2 != nil {
		return false, "执行 " + action + " 失败: " + err2.Error()
	}
	return true, ""
}

// rebuild starts a rebuild job on the panel's Manager and reports task-log
// progress plus the final result (with a refreshed instance list).
func (l *localAgent) rebuild(ctx context.Context, cmd agenthub.Command, report func(bool, string, map[string]any)) {
	pat, err := l.openPAT(cmd)
	if err != nil {
		report(false, err.Error(), nil)
		return
	}
	id, _ := agentIntParam(cmd.Params["id"])
	if id <= 0 {
		report(false, "无效的实例 ID", nil)
		return
	}
	script, _ := cmd.Params["script"].(string)
	reservedIP, _ := cmd.Params["reserved_ip"].(string)

	taskID, err := l.s.Tasks.Start(id, script, reservedIP, pat, "本地面板")
	if err != nil {
		report(false, err.Error(), nil)
		return
	}
	// Poll the task; report the newest log line as progress.
	lastLen := 0
	for {
		task, gerr := l.s.Tasks.Get(taskID)
		if gerr != nil {
			report(false, gerr.Error(), nil)
			return
		}
		if len(task.Log) > lastLen {
			report(false, "", map[string]any{"progress": task.Log[0]})
			lastLen = len(task.Log)
		}
		if task.Status != StatusRunning {
			if task.Status == StatusSuccess {
				report(true, "", map[string]any{"instances": l.refreshInstances(ctx, pat)})
			} else {
				report(false, task.Err, nil)
			}
			return
		}
		select {
		case <-ctx.Done():
			report(false, "已取消", nil)
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// refreshInstances best-effort lists the account's instances for the final
// rebuild payload, mirroring the remote agent's result shape.
func (l *localAgent) refreshInstances(ctx context.Context, pat string) []any {
	insts, err := l.s.clientFor(pat).ListInstances(ctx)
	if err != nil {
		return []any{}
	}
	out := make([]any, 0, len(insts))
	for _, in := range insts {
		out = append(out, in)
	}
	return out
}

// agentIntParam coerces a JSON number into an int.
func agentIntParam(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	}
	return 0, false
}
