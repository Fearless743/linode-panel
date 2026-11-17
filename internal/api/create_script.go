package api

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"linode-panel/internal/lish"
)

// 创建实例时开机脚本的执行状态。脚本在实例创建后由后台协程执行，所以状态
// 存在内存里（面板重启后丢失），随 GET /api/instances/{id}/script 返回。
const (
	ScriptRunPending = "pending" // 已排队，等待实例启动
	ScriptRunRunning = "running" // 正在通过 Lish 控制台执行
	ScriptRunSuccess = "success"
	ScriptRunFailed  = "failed"
)

const (
	// bootScriptBootTimeout bounds waiting for the new instance to reach
	// "running".
	bootScriptBootTimeout = 10 * time.Minute
	// bootScriptLishTimeout bounds the Lish phase (connect retries + script run).
	bootScriptLishTimeout = 10 * time.Minute
	// bootScriptRunTimeout bounds one console attempt (including the script).
	bootScriptRunTimeout = 5 * time.Minute
	// bootScriptRetryDelay is the pause between console connect retries while
	// the instance finishes booting.
	bootScriptRetryDelay = 5 * time.Second
)

// scriptRunState is the observable state of one create-time boot-script run.
type scriptRunState struct {
	State      string `json:"state"`
	Error      string `json:"error,omitempty"`
	Host       string `json:"host,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
}

// bootScriptRun returns a copy of the last recorded run state for an instance.
func (s *Server) bootScriptRun(instanceID int) (scriptRunState, bool) {
	s.bootScriptMu.Lock()
	defer s.bootScriptMu.Unlock()
	st, ok := s.bootScriptRuns[instanceID]
	return st, ok
}

// updateBootScriptRun mutates the recorded run state for instanceID.
func (s *Server) updateBootScriptRun(instanceID int, mutate func(*scriptRunState)) {
	s.bootScriptMu.Lock()
	defer s.bootScriptMu.Unlock()
	if s.bootScriptRuns == nil {
		s.bootScriptRuns = make(map[int]scriptRunState)
	}
	st := s.bootScriptRuns[instanceID]
	mutate(&st)
	s.bootScriptRuns[instanceID] = st
}

// lishRunner returns the Lish runner for boot scripts (injectable in tests).
func (s *Server) lishRunner() func(context.Context, lish.Options, string) (lish.Result, error) {
	if s.BootScriptRun != nil {
		return s.BootScriptRun
	}
	return lish.Run
}

// startBootScript launches the background execution of a freshly created
// instance's boot script. It returns immediately; progress is recorded in
// bootScriptRuns. Auth is by the root password typed into the Lish console.
func (s *Server) startBootScript(instanceID int, pat string, password string, script string) {
	now := time.Now().UTC().Format(time.RFC3339)
	s.updateBootScriptRun(instanceID, func(st *scriptRunState) {
		*st = scriptRunState{State: ScriptRunPending, StartedAt: now}
	})
	go s.runBootScript(instanceID, pat, password, script)
}

// runBootScript waits for the instance to boot, then runs its script on the
// Lish serial console with the root password. Unlike SSH this needs neither
// a public IP nor an open sshd port.
//
// Connection failures are retried until bootScriptLishTimeout because the
// console may not be ready the moment the instance reports "running".
func (s *Server) runBootScript(instanceID int, pat string, password string, script string) {
	api := s.clientFor(pat)

	bootCtx, cancelBoot := context.WithTimeout(context.Background(), bootScriptBootTimeout)
	err := s.waitForInstanceStatus(bootCtx, api, instanceID, "running")
	cancelBoot()
	if err != nil {
		s.failBootScript(instanceID, fmt.Errorf("等待实例启动失败: %w", err))
		return
	}

	s.updateBootScriptRun(instanceID, func(st *scriptRunState) {
		st.State = ScriptRunRunning
		st.Host = fmt.Sprintf("lish://instance/%d", instanceID)
	})

	lishCtx, cancelLish := context.WithTimeout(context.Background(), bootScriptLishTimeout)
	defer cancelLish()
	run := s.lishRunner()
	for {
		res, runErr := run(lishCtx, lish.Options{
			InstanceID: instanceID,
			PAT:        pat,
			Password:   password,
			APIBase:    s.APIBase,
			Timeout:    bootScriptRunTimeout,
		}, script)
		if runErr == nil {
			s.updateBootScriptRun(instanceID, func(st *scriptRunState) { st.Host = res.Host })
			if res.Exit != 0 {
				detail := res.Stderr
				if detail == "" {
					detail = res.Stdout
				}
				s.failBootScript(instanceID, fmt.Errorf("开机脚本退出码 %d: %s", res.Exit, detail))
				return
			}
			s.updateBootScriptRun(instanceID, func(st *scriptRunState) {
				st.State = ScriptRunSuccess
				st.Error = ""
				st.FinishedAt = time.Now().UTC().Format(time.RFC3339)
			})
			slog.Info("开机脚本执行完成", "instance_id", instanceID, "host", res.Host)
			return
		}
		// Only connectivity failures are worth retrying: once the console
		// login started, a transport error may have interrupted a partially
		// applied script and re-running it could be unsafe.
		if lish.IsAuthError(runErr) {
			s.failBootScript(instanceID, fmt.Errorf("root 密码不正确或控制台登录失败: %w", runErr))
			return
		}
		if !lish.IsConnectError(runErr) {
			s.failBootScript(instanceID, fmt.Errorf("Lish 执行开机脚本失败: %w", runErr))
			return
		}
		select {
		case <-lishCtx.Done():
			s.failBootScript(instanceID, fmt.Errorf("Lish 连接实例超时: %w", runErr))
			return
		case <-time.After(bootScriptRetryDelay):
		}
	}
}

func (s *Server) failBootScript(instanceID int, err error) {
	s.updateBootScriptRun(instanceID, func(st *scriptRunState) {
		st.State = ScriptRunFailed
		st.Error = err.Error()
		st.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	})
	slog.Warn("开机脚本执行失败", "instance_id", instanceID, "err", err)
}

// waitForInstanceStatus polls api until the instance reaches want or ctx ends.
func (s *Server) waitForInstanceStatus(ctx context.Context, api LinodeAPI, id int, want string) error {
	interval := s.BootScriptPollInterval
	if interval <= 0 {
		interval = 3 * time.Second
	}
	for {
		inst, err := api.GetInstance(ctx, id)
		if err != nil {
			return err
		}
		if inst.Status == want {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("等待实例 %d 状态 %s 超时（当前 %s）", id, want, inst.Status)
		case <-time.After(interval):
		}
	}
}
