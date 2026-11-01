package api

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"linode-panel/internal/sshrun"
)

// 创建实例时开机脚本的执行状态。脚本在实例创建后由后台协程执行，所以状态
// 存在内存里（面板重启后丢失），随 GET /api/instances/{id}/script 返回。
const (
	ScriptRunPending = "pending" // 已排队，等待实例启动
	ScriptRunRunning = "running" // 正在通过 SSH 执行
	ScriptRunSuccess = "success"
	ScriptRunFailed  = "failed"
)

const (
	// bootScriptBootTimeout bounds waiting for the new instance to reach
	// "running".
	bootScriptBootTimeout = 10 * time.Minute
	// bootScriptSSHTimeout bounds the SSH phase (connect retries + script run).
	bootScriptSSHTimeout = 10 * time.Minute
	// bootScriptRunTimeout bounds one SSH attempt (including the script).
	bootScriptRunTimeout = 5 * time.Minute
	// bootScriptRetryDelay is the pause between SSH connect retries while the
	// instance finishes booting (sshd not listening yet).
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

// sshRunner returns the SSH runner for boot scripts (injectable in tests).
func (s *Server) sshRunner() func(context.Context, sshrun.Options, string) (sshrun.Result, error) {
	if s.BootScriptRun != nil {
		return s.BootScriptRun
	}
	return sshrun.Run
}

// startBootScript launches the background execution of a freshly created
// instance's boot script. It returns immediately; progress is recorded in
// bootScriptRuns.
func (s *Server) startBootScript(instanceID int, pat string, privPEM []byte, script string) {
	now := time.Now().UTC().Format(time.RFC3339)
	s.updateBootScriptRun(instanceID, func(st *scriptRunState) {
		*st = scriptRunState{State: ScriptRunPending, StartedAt: now}
	})
	go s.runBootScript(instanceID, pat, privPEM, script)
}

// runBootScript waits for the instance to boot, then runs its script over SSH.
//
// The instance is created with a one-off ed25519 public key in
// authorized_keys, so root login by key works before any password is used.
// Connection failures are retried until bootScriptSSHTimeout because sshd may
// not be listening the moment the instance reports "running".
func (s *Server) runBootScript(instanceID int, pat string, privPEM []byte, script string) {
	api := s.clientFor(pat)

	bootCtx, cancelBoot := context.WithTimeout(context.Background(), bootScriptBootTimeout)
	err := s.waitForInstanceStatus(bootCtx, api, instanceID, "running")
	cancelBoot()
	if err != nil {
		s.failBootScript(instanceID, fmt.Errorf("等待实例启动失败: %w", err))
		return
	}

	ips, err := api.GetInstanceIPs(context.Background(), instanceID)
	if err != nil {
		s.failBootScript(instanceID, fmt.Errorf("获取实例 IP 失败: %w", err))
		return
	}
	host := ips.PublicIPv4()
	if host == "" {
		host = ips.SLAACv6()
	}
	if host == "" {
		s.failBootScript(instanceID, fmt.Errorf("实例没有可用的公网 IP"))
		return
	}

	s.updateBootScriptRun(instanceID, func(st *scriptRunState) {
		st.State = ScriptRunRunning
		st.Host = host
	})

	sshCtx, cancelSSH := context.WithTimeout(context.Background(), bootScriptSSHTimeout)
	defer cancelSSH()
	run := s.sshRunner()
	for {
		res, runErr := run(sshCtx, sshrun.Options{
			Host:               host,
			Port:               s.sshPort(),
			PrivateKey:         privPEM,
			InsecureSkipVerify: true,
			Timeout:            bootScriptRunTimeout,
		}, script)
		if runErr == nil {
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
			slog.Info("开机脚本执行完成", "instance_id", instanceID, "host", host)
			return
		}
		// Only connectivity failures are worth retrying: once the session
		// started, a transport error may have interrupted a partially applied
		// script and re-running it could be unsafe.
		if !isSSHConnectError(runErr) {
			s.failBootScript(instanceID, fmt.Errorf("SSH 执行开机脚本失败: %w", runErr))
			return
		}
		select {
		case <-sshCtx.Done():
			s.failBootScript(instanceID, fmt.Errorf("SSH 连接实例超时: %w", runErr))
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

// isSSHConnectError reports whether err happened before the remote command
// started (TCP dial or SSH handshake), which makes a retry safe.
func isSSHConnectError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "sshrun: dial") || strings.Contains(msg, "sshrun: handshake")
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
