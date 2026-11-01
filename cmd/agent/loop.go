package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"linode-panel/internal/agenthub"
)

// agent is the poll/execute loop state.
type agent struct {
	pc       *panelClient
	hostname string
	exec     *agenthub.Executor
	log      *slog.Logger
}

// run polls the panel forever, executing commands in order. It returns only
// on a fatal, unrecoverable error (e.g. the panel rejected the agent token).
func (a *agent) run(ctx context.Context) error {
	backoff := 5 * time.Second
	for {
		cmds, err := a.pc.poll(ctx, a.hostname)
		if err != nil {
			// An invalid/revoked token cannot be recovered without a new
			// registration code, so stop and let the operator restart the
			// agent with --code.
			if errors.Is(err, errUnauthorized) {
				return fmt.Errorf("面板拒绝身份（token 无效或被删除），请用 --code 重新启动 agent: %w", err)
			}
			a.log.Warn("轮询失败，退避", "err", err, "backoff", backoff)
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

// execute runs one command with the shared executor and reports the result
// (with retries). Progress updates are reported in flight.
func (a *agent) execute(ctx context.Context, cmd pollCommand) error {
	a.log.Info("执行命令", "command_id", cmd.ID, "type", cmd.Type)
	var reportErr error
	a.exec.Execute(ctx, agenthub.Command{ID: cmd.ID, Type: cmd.Type, Params: cmd.Params},
		func(ok bool, errMsg string, payload map[string]any) {
			if err := a.reportWithRetry(cmd.ID, ok, errMsg, payload); err != nil {
				reportErr = err
			}
		})
	return reportErr
}

// reportWithRetry posts the result, retrying up to 3 more times with
// 1s/2s/4s backoff. Failures are logged and ultimately abandoned.
func (a *agent) reportWithRetry(cmdID int64, ok bool, errMsg string, payload map[string]any) error {
	delays := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	var lastErr error
	for attempt := 0; attempt <= len(delays); attempt++ {
		if attempt > 0 {
			time.Sleep(delays[attempt-1])
		}
		lastErr = a.reportResult(context.Background(), cmdID, ok, errMsg, payload)
		if lastErr == nil {
			return nil
		}
		a.log.Warn("上报结果失败，重试", "command_id", cmdID, "attempt", attempt+1, "err", lastErr)
	}
	return fmt.Errorf("上报结果失败（已重试 %d 次）: %w", len(delays), lastErr)
}

// reportResult posts one result to the panel.
func (a *agent) reportResult(ctx context.Context, cmdID int64, ok bool, errMsg string, payload map[string]any) error {
	return a.pc.reportResult(ctx, cmdID, ok, errMsg, payload)
}
