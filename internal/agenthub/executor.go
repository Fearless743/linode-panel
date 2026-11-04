package agenthub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"linode-panel/internal/linode"
	"linode-panel/internal/sshrun"
)

// LinodeAPI is the subset of the Linode client used by the executor.
type LinodeAPI interface {
	ListInstances(ctx context.Context) ([]linode.Instance, error)
	GetInstance(ctx context.Context, id int) (linode.Instance, error)
	Boot(ctx context.Context, id int) error
	Shutdown(ctx context.Context, id int) error
	Reboot(ctx context.Context, id int) error
	GetInstanceIPs(ctx context.Context, id int) (linode.InstanceIPs, error)
	CreateInstance(ctx context.Context, opts linode.CreateOptions) (linode.Instance, error)
	DeleteInstance(ctx context.Context, id int) error
}

// RunFn runs the boot script over SSH with the root password.
type RunFn func(ctx context.Context, host string, port int, password string, script string) error

// ProbeFn verifies the instance's SSH port is reachable.
type ProbeFn func(ctx context.Context, host string, port int) error

// Executor carries the agent's credentials and injectable seams. It is the
// shared command executor used by both remote agents (cmd/agent) and the
// panel's own self-agent (internal/api/localagent.go).
type Executor struct {
	// APIBase is the Linode API v4 base URL used by per-PAT clients.
	APIBase string
	// SSHPort is the SSH port used for boot scripts and probes.
	SSHPort int
	// Run executes the boot script over SSH. Defaults to sshrun.Run.
	Run RunFn
	// Probe verifies the instance's SSH port is reachable.
	Probe ProbeFn
	// NewAPI builds a LinodeAPI for one PAT. Defaults to linode.New with APIBase applied.
	NewAPI func(pat string) LinodeAPI
	// Log is the logger used by the executor.
	Log *slog.Logger
}

// Reporter receives a command's final result, or an in-flight progress update
// (ok=false with payload["progress"]).
type Reporter func(ok bool, errMsg string, payload map[string]any)

// NewExecutor creates an Executor with default values.
func NewExecutor() *Executor {
	return &Executor{
		APIBase: "https://api.linode.com/v4",
		SSHPort: 22,
		Run:     DefaultRunFn,
	}
}

// DefaultRunFn wraps sshrun.Run with the Executor's signature.
func DefaultRunFn(ctx context.Context, host string, port int, password string, script string) error {
	res, err := sshrun.Run(ctx, sshrun.Options{
		Host:               host,
		Port:               port,
		Password:           password,
		InsecureSkipVerify: true,
		Timeout:            5 * time.Minute,
	}, script)
	if err != nil {
		return err
	}
	if res.Exit != 0 {
		detail := res.Stderr
		if detail == "" {
			detail = res.Stdout
		}
		return fmt.Errorf("SSH 脚本退出码 %d: %s", res.Exit, detail)
	}
	return nil
}

// API returns a LinodeAPI for the given PAT, using NewAPI if set.
func (e *Executor) API(pat string) LinodeAPI {
	if e.NewAPI != nil {
		return e.NewAPI(pat)
	}
	c := linode.New(pat)
	if e.APIBase != "" {
		c.BaseURL = e.APIBase
	}
	return c
}

// IntParam coerces a JSON-decoded number into an int.
func IntParam(v any) (int, bool) {
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

// isNotFound reports whether err is a Linode API 404 (the instance is gone).
func isNotFound(err error) bool {
	var apiErr *linode.APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

// usableFallback reports whether fallback carries enough to create a
// replacement instance.
func usableFallback(fallback *linode.Instance) bool {
	return fallback != nil && fallback.Region != "" && fallback.Type != "" && fallback.Image != ""
}

// fallbackInstanceParam decodes the "old_instance" param of a rebuild command.
// The panel's self-agent passes a linode.Instance directly; a remote agent
// receives the JSON-decoded map form.
func fallbackInstanceParam(v any) *linode.Instance {
	switch t := v.(type) {
	case linode.Instance:
		return &t
	case *linode.Instance:
		return t
	case map[string]any:
		return &linode.Instance{
			Region: stringParam(t["region"]),
			Type:   stringParam(t["type"]),
			Image:  stringParam(t["image"]),
			Label:  stringParam(t["label"]),
			Tags:   stringSliceParam(t["tags"]),
		}
	}
	return nil
}

// stringParam coerces a JSON-decoded value into a string.
func stringParam(v any) string {
	s, _ := v.(string)
	return s
}

// stringSliceParam coerces a JSON-decoded array into a []string.
func stringSliceParam(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// ListInstances lists the instances of a single account (PAT).
func (e *Executor) ListInstances(ctx context.Context, pat string) ([]linode.Instance, error) {
	insts, err := e.API(pat).ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	if insts == nil {
		insts = []linode.Instance{}
	}
	return insts, nil
}

// Power performs one power action against a single account (PAT).
func (e *Executor) Power(ctx context.Context, pat, action string, id int) error {
	if pat == "" {
		return errors.New("命令未携带 PAT")
	}
	api := e.API(pat)
	switch action {
	case ActionBoot:
		return api.Boot(ctx, id)
	case ActionShutdown:
		return api.Shutdown(ctx, id)
	case ActionReboot:
		return api.Reboot(ctx, id)
	default:
		return fmt.Errorf("未知电源操作: %s", action)
	}
}

// Execute runs one command with the executor and reports its result through
// report. Long-running rebuilds emit progress updates (ok=false with a
// payload["progress"] step string) before the final result. This is the single
// implementation shared by remote agents and the panel's own self-agent.
func (e *Executor) Execute(ctx context.Context, cmd Command, report Reporter) {
	pat, _ := cmd.Params["pat"].(string)
	switch cmd.Type {
	case CmdList:
		if pat == "" {
			report(false, "命令未携带 PAT", nil)
			return
		}
		insts, err := e.ListInstances(ctx, pat)
		if err != nil {
			report(false, err.Error(), nil)
			return
		}
		report(true, "", map[string]any{"instances": insts})
	case CmdPower:
		if pat == "" {
			report(false, "命令未携带 PAT", nil)
			return
		}
		action, _ := cmd.Params["action"].(string)
		id, ok := IntParam(cmd.Params["id"])
		if !ok || id <= 0 {
			report(false, "无效的实例 ID", nil)
			return
		}
		if err := e.Power(ctx, pat, action, id); err != nil {
			report(false, err.Error(), nil)
			return
		}
		report(true, "", map[string]any{})
	case CmdReboot:
		if pat == "" {
			report(false, "命令未携带 PAT", nil)
			return
		}
		id, ok := IntParam(cmd.Params["id"])
		if !ok || id <= 0 {
			report(false, "无效的实例 ID", nil)
			return
		}
		script, _ := cmd.Params["script"].(string)
		rootPass, _ := cmd.Params["root_password"].(string)
		progress := func(step string) {
			report(false, "", map[string]any{"progress": step})
		}
		if newID, _, err := e.Rebuild(ctx, id, script, rootPass, pat, fallbackInstanceParam(cmd.Params["old_instance"]), progress); err != nil {
			// Even a failed rebuild may already have created the replacement
			// instance (e.g. the boot script failed). Report its id so the
			// panel can still make it inherit the instance's configuration.
			payload := map[string]any{}
			if newID > 0 {
				payload["new_instance_id"] = newID
			}
			report(false, err.Error(), payload)
			return
		} else {
			insts, err := e.ListInstances(ctx, pat)
			if err != nil {
				report(false, err.Error(), nil)
				return
			}
			// new_instance_id lets the panel re-point its stable cache row to
			// the replacement instance, so per-instance configuration is
			// inherited even when the rebuild runs on a remote agent.
			report(true, "", map[string]any{"instances": insts, "new_instance_id": newID})
		}
	case CmdDelete:
		if pat == "" {
			report(false, "命令未携带 PAT", nil)
			return
		}
		id, ok := IntParam(cmd.Params["id"])
		if !ok || id <= 0 {
			report(false, "无效的实例 ID", nil)
			return
		}
		if err := e.API(pat).DeleteInstance(ctx, id); err != nil {
			report(false, err.Error(), nil)
			return
		}
		report(true, "", map[string]any{})
	default:
		report(false, "未知命令类型: "+cmd.Type, nil)
	}
}

// Rebuild executes the full rebuild orchestration:
// shutdown old → delete → create (root password, booted) → wait running →
// sshrun script (root password) → probe SSH port. progress (when non-nil) is
// called with a Chinese label before each step. fallback, when non-nil, is the
// panel's cached snapshot of the old instance; it is used to create the
// replacement directly when the old instance no longer exists upstream (404),
// in which case shutdown/delete are skipped. It returns the replacement
// instance's Linode id (0 when it was never created) and its host address.
func (e *Executor) Rebuild(ctx context.Context, id int, script, rootPass, pat string, fallback *linode.Instance, progress func(step string)) (int, string, error) {
	api := e.API(pat)
	step := func(n int, format string, args ...any) {
		label := fmt.Sprintf("step %d: %s", n, fmt.Sprintf(format, args...))
		if progress != nil {
			progress(label)
		}
		if e.Log != nil {
			e.Log.Info(label)
		}
	}

	step(0, "读取旧实例 %d", id)
	old, err := api.GetInstance(ctx, id)
	missing := false
	if err != nil {
		if !isNotFound(err) || !usableFallback(fallback) {
			return 0, "", fmt.Errorf("读取旧实例失败: %w", err)
		}
		old = *fallback
		missing = true
		step(0, "旧实例 %d 已不存在（404），使用面板缓存直接新建 (region=%s type=%s image=%s)",
			id, old.Region, old.Type, old.Image)
	}

	if !missing {
		step(1, "关机旧实例 %d", id)
		if err := api.Shutdown(ctx, id); err != nil {
			return 0, "", fmt.Errorf("关机旧实例失败: %w", err)
		}

		step(2, "删除旧实例 %d", id)
		if err := api.DeleteInstance(ctx, id); err != nil {
			return 0, "", fmt.Errorf("删除旧实例失败: %w", err)
		}
	} else {
		step(1, "跳过旧实例 %d 的关机/删除（已不存在）", id)
	}

	booted := true
	step(3, "创建新实例 (region=%s type=%s image=%s)", old.Region, old.Type, old.Image)
	newInst, err := api.CreateInstance(ctx, linode.CreateOptions{
		Region:   old.Region,
		Type:     old.Type,
		Image:    old.Image,
		Label:    old.Label,
		Tags:     old.Tags,
		RootPass: rootPass,
		Booted:   &booted,
	})
	if err != nil {
		return 0, "", fmt.Errorf("创建新实例失败: %w", err)
	}
	newID := newInst.ID

	step(4, "等待新实例 %d 运行", newID)
	if err := e.waitStatus(ctx, api, newID, "running"); err != nil {
		return newID, "", err
	}

	step(5, "获取新实例 IP 并执行开机脚本")
	ips, err := api.GetInstanceIPs(ctx, newID)
	if err != nil {
		return newID, "", fmt.Errorf("获取新实例 IP 失败: %w", err)
	}
	host := ips.PublicIPv4()
	if host == "" {
		host = ips.SLAACv6()
	}
	if host == "" {
		return newID, "", fmt.Errorf("新实例没有可用的公网 IP")
	}
	if e.Run == nil {
		return newID, "", fmt.Errorf("SSH 运行器未配置")
	}
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := e.Run(runCtx, host, e.SSHPort, rootPass, script); err != nil {
		return newID, host, fmt.Errorf("SSH 执行开机脚本失败: %w", err)
	}

	step(6, "探测 SSH 端口 %s:%d", host, e.SSHPort)
	if err := e.ProbePort(ctx, host); err != nil {
		return newID, host, fmt.Errorf("SSH 端口探测失败: %w", err)
	}
	return newID, host, nil
}

// waitStatus polls the instance until it reaches want or the timeout elapses.
func (e *Executor) waitStatus(ctx context.Context, api LinodeAPI, id int, want string) error {
	deadline := time.Now().Add(10 * time.Minute)
	for {
		inst, err := api.GetInstance(ctx, id)
		if err != nil {
			return fmt.Errorf("查询实例状态失败: %w", err)
		}
		if inst.Status == want {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待实例 %d 状态 %s 超时（当前 %s）", id, want, inst.Status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// ProbePort attempts a TCP connection to host:sshPort, bounded by 3s.
func (e *Executor) ProbePort(ctx context.Context, host string) error {
	if e.Probe != nil {
		return e.Probe(ctx, host, e.SSHPort)
	}
	addr := net.JoinHostPort(host, strconv.Itoa(e.SSHPort))
	d := net.Dialer{Timeout: 3 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}
