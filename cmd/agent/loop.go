package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"linode-panel/internal/agentcrypto"
	"linode-panel/internal/agenthub"
	"linode-panel/internal/linode"
)

// agent is the poll/execute loop state.
type agent struct {
	pc        *panelClient
	st        *state
	statePath string
	key       []byte // AES key derived from the registration code
	code      string // registration code for (re)registration
	hostname  string
	exec      *executor
	log       *slog.Logger
}

// cmdSetPAT is an additive command type: the panel hands the agent a PAT
// encrypted with the shared key; the agent decrypts it, stores it locally
// (re-encrypted at rest) and acknowledges.
const cmdSetPAT = "set_pat"

// run polls the panel forever, executing commands in order. It returns only
// on a fatal, unrecoverable error.
func (a *agent) run(ctx context.Context) error {
	backoff := 5 * time.Second
	for {
		cmds, err := a.pc.poll(ctx, a.hostname)
		if err != nil {
			if errors.Is(err, errUnauthorized) {
				a.log.Warn("面板拒绝身份，尝试重新注册")
				a.st.AgentToken = ""
				a.pc.token = ""
				if a.registrationCode() == "" {
					return errors.New("面板拒绝身份且缺少注册码，无法重新注册")
				}
				if err := ensureToken(ctx, a.st, a.statePath, a.pc, a.registrationCode(), a.hostname); err != nil {
					a.log.Warn("重新注册失败", "err", err)
				} else {
					// Re-registered: poll immediately, no backoff.
					continue
				}
			} else {
				a.log.Warn("轮询失败，退避", "err", err, "backoff", backoff)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			backoff = nextBackoff(backoff)
			continue
		}
		backoff = 5 * time.Second
		for _, cmd := range cmds {
			if err := a.execute(ctx, cmd); err != nil {
				a.log.Error("执行命令失败", "command_id", cmd.ID, "type", cmd.Type, "err", err)
			}
		}
	}
}

func nextBackoff(cur time.Duration) time.Duration {
	switch cur {
	case 5 * time.Second:
		return 10 * time.Second
	case 10 * time.Second:
		return 30 * time.Second
	default:
		return 30 * time.Second
	}
}

// execute runs one command and reports the result (with retries).
func (a *agent) execute(ctx context.Context, cmd pollCommand) error {
	a.log.Info("执行命令", "command_id", cmd.ID, "type", cmd.Type)
	var ok bool
	var errMsg string
	var payload map[string]any
	switch cmd.Type {
	case agenthub.CmdList:
		insts, err := a.exec.listInstances(ctx)
		if err != nil {
			errMsg = err.Error()
		} else {
			payload = map[string]any{"instances": insts}
			ok = true
		}
	case agenthub.CmdPower:
		action, _ := cmd.Params["action"].(string)
		id, _ := intParam(cmd.Params["id"])
		// Prefer the PAT sealed into the command (sent by the panel);
		// fall back to locally stored PATs when absent.
		if sealedPAT, _ := cmd.Params["sealed_pat"].(string); sealedPAT != "" {
			if pat, err := agentcrypto.Open(a.key, sealedPAT); err == nil {
				if err := a.exec.powerActionWith(ctx, []string{string(pat)}, action, id); err != nil {
					errMsg = err.Error()
				} else {
					payload = map[string]any{}
					ok = true
				}
				break
			}
		}
		if err := a.exec.powerAction(ctx, action, id); err != nil {
			errMsg = err.Error()
		} else {
			payload = map[string]any{}
			ok = true
		}
	case agenthub.CmdReboot:
		id, _ := intParam(cmd.Params["id"])
		script, _ := cmd.Params["script"].(string)
		reservedIP, _ := cmd.Params["reserved_ip"].(string)
		sealedPAT, _ := cmd.Params["sealed_pat"].(string)
		pat, err := agentcrypto.Open(a.key, sealedPAT)
		if err != nil {
			errMsg = fmt.Sprintf("解密 PAT 失败: %v", err)
			break
		}
		insts, err := a.doRebuild(ctx, cmd.ID, id, script, reservedIP, string(pat))
		if err != nil {
			errMsg = err.Error()
		} else {
			payload = map[string]any{"instances": insts}
			ok = true
		}
	case cmdSetPAT:
		sealed, _ := cmd.Params["sealed_pat"].(string)
		if err := a.setPAT(sealed); err != nil {
			errMsg = err.Error()
		} else {
			payload = map[string]any{}
			ok = true
		}
	default:
		errMsg = fmt.Sprintf("未知命令类型: %s", cmd.Type)
	}
	return a.reportWithRetry(cmd.ID, ok, errMsg, payload)
}

// doRebuild runs the rebuild and, after the instance is running, refreshes
// the instance list for the result payload.
func (a *agent) doRebuild(ctx context.Context, cmdID int64, id int, script, reservedIP, pat string) ([]linode.Instance, error) {
	// Progress reports carry the same command_id with ok=false so the panel
	// knows the command is still in flight.
	a.exec.progress = func(step string) {
		a.reportProgress(cmdID, step)
	}
	defer func() { a.exec.progress = nil }()
	if _, err := a.exec.rebuild(ctx, id, script, reservedIP, pat); err != nil {
		return nil, err
	}
	return a.exec.listInstances(ctx)
}

// setPAT decrypts the sealed PAT, updates the in-memory list and persists it
// re-encrypted at rest with the locally-derived key.
func (a *agent) setPAT(sealed string) error {
	plain, err := agentcrypto.Open(a.key, sealed)
	if err != nil {
		return fmt.Errorf("解密 PAT 失败: %w", err)
	}
	pat := string(plain)
	a.exec.pats = replacePAT(a.exec.pats, pat)
	sealedLocal, err := agentcrypto.Seal(a.key, []byte(pat))
	if err != nil {
		return fmt.Errorf("加密本地 PAT 失败: %w", err)
	}
	a.st.SealedPAT = sealedLocal
	if err := saveState(a.statePath, *a.st); err != nil {
		return fmt.Errorf("保存本地 PAT 失败: %w", err)
	}
	a.log.Info("本地 PAT 已更新")
	return nil
}

func replacePAT(pats []string, pat string) []string {
	for _, p := range pats {
		if p == pat {
			return pats
		}
	}
	return append(pats, pat)
}

// reportProgress posts an in-flight progress update ({ok:false,
// payload:{progress:...}}) for a long-running command.
func (a *agent) reportProgress(cmdID int64, step string) {
	_ = a.reportWithRetry(cmdID, false, "", map[string]any{"progress": step})
}

// reportWithRetry posts the final result, retrying up to 3 more times with
// 1s/2s/4s backoff. Failures are logged and ultimately abandoned.
func (a *agent) reportWithRetry(cmdID int64, ok bool, errMsg string, payload map[string]any) error {
	delays := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	var lastErr error
	for attempt := 0; attempt <= len(delays); attempt++ {
		if attempt > 0 {
			time.Sleep(delays[attempt-1])
		}
		lastErr = a.pc.reportResult(context.Background(), cmdID, ok, errMsg, payload)
		if lastErr == nil {
			return nil
		}
		a.log.Warn("上报结果失败，重试", "command_id", cmdID, "attempt", attempt+1, "err", lastErr)
	}
	return fmt.Errorf("上报结果失败（已重试 %d 次）: %w", len(delays), lastErr)
}

// intParam coerces a JSON number into an int.
func intParam(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	case int:
		return n, true
	}
	return 0, false
}

// registrationCode returns the code used to (re)register, kept on the agent.
func (a *agent) registrationCode() string { return a.code }

// loadPATsFromState decrypts the locally stored PAT (if any) at startup.
func (a *agent) loadPATsFromState() error {
	if a.st.SealedPAT == "" {
		return nil
	}
	plain, err := agentcrypto.Open(a.key, a.st.SealedPAT)
	if err != nil {
		return fmt.Errorf("解密本地 PAT 失败: %w", err)
	}
	a.exec.pats = []string{string(plain)}
	return nil
}

// fatal writes a Chinese error to stderr and exits non-zero.
func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "错误: "+format+"\n", args...)
	os.Exit(1)
}
