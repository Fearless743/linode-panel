package agenthub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
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
	GetConfigID(ctx context.Context, id int) (int, error)
	UpdateConfigNetworkHelper(ctx context.Context, id, configID int, enabled bool) error
	AssignIP(ctx context.Context, region, address string, linodeID int) error
	AttachAllowAllFirewall(ctx context.Context, region string, linodeID int) error
}

// RunFn runs the boot script over SSH.
type RunFn func(ctx context.Context, host string, port int, privPEM []byte, script string) error

// ProbeFn verifies the instance's SSH port is reachable.
type ProbeFn func(ctx context.Context, host string, port int) error

// Executor carries the agent's credentials and injectable seams. It is the
// shared command executor used by both remote agents (cmd/agent) and the
// panel's own self-agent (internal/api/localagent.go).
type Executor struct {
	// PATs holds the locally-saved Linode PATs (plaintext, in memory).
	PATs []string
	// APIBase is the Linode API v4 base URL used by per-PAT clients.
	APIBase string
	// SSHPort is the SSH port used for boot scripts and probes.
	SSHPort int
	// Run executes the boot script over SSH. Defaults to sshrun.Run.
	Run RunFn
	// Probe verifies the instance's SSH port is reachable.
	Probe ProbeFn
	// Progress is an optional callback invoked before each rebuild step.
	Progress func(step string)
	// NewAPI builds a LinodeAPI for one PAT. Defaults to linode.New with APIBase applied.
	NewAPI func(pat string) LinodeAPI
	// Log is the logger used by the executor.
	Log *slog.Logger
}

// NewExecutor creates an Executor with default values.
func NewExecutor() *Executor {
	return &Executor{
		APIBase: "https://api.linode.com/v4",
		SSHPort: 22,
		Run:     DefaultRunFn,
	}
}

// DefaultRunFn wraps sshrun.Run with the Executor's signature.
func DefaultRunFn(ctx context.Context, host string, port int, privPEM []byte, script string) error {
	res, err := sshrun.Run(ctx, sshrun.Options{
		Host:               host,
		Port:               port,
		PrivateKey:         privPEM,
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

// IsNotFoundOrForbidden reports whether a Linode API error is 404 or 403.
func IsNotFoundOrForbidden(err error) bool {
	var apiErr *linode.APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == 404 || apiErr.StatusCode == 403
	}
	return false
}

// ListInstances aggregates all instances across every local PAT. A PAT that
// fails (bad token, network) is skipped with a warning. No PATs → empty list.
func (e *Executor) ListInstances(ctx context.Context) ([]linode.Instance, error) {
	if len(e.PATs) == 0 {
		return []linode.Instance{}, nil
	}
	seen := map[int]bool{}
	var all []linode.Instance
	for _, pat := range e.PATs {
		api := e.API(pat)
		insts, err := api.ListInstances(ctx)
		if err != nil {
			if e.Log != nil {
				e.Log.Warn("list: 跳过失败的 PAT", "err", err)
			}
			continue
		}
		for _, in := range insts {
			if !seen[in.ID] {
				seen[in.ID] = true
				all = append(all, in)
			}
		}
	}
	if all == nil {
		all = []linode.Instance{}
	}
	return all, nil
}

// PowerAction performs one power action, trying each stored PAT until one
// succeeds. A PAT that returns 404/403 is skipped; any other error is a real
// failure.
func (e *Executor) PowerAction(ctx context.Context, action string, id int) error {
	return e.PowerActionWith(ctx, e.PATs, action, id)
}

// PowerActionWith is PowerAction over an explicit PAT list.
func (e *Executor) PowerActionWith(ctx context.Context, pats []string, action string, id int) error {
	if len(pats) == 0 {
		return errors.New("没有可用的 PAT")
	}
	var lastErr error
	for _, pat := range pats {
		api := e.API(pat)
		var err error
		switch action {
		case ActionBoot:
			err = api.Boot(ctx, id)
		case ActionShutdown:
			err = api.Shutdown(ctx, id)
		case ActionReboot:
			err = api.Reboot(ctx, id)
		default:
			return fmt.Errorf("未知电源操作: %s", action)
		}
		if err == nil {
			return nil
		}
		if IsNotFoundOrForbidden(err) {
			lastErr = err
			continue
		}
		return fmt.Errorf("执行 %s 失败: %w", action, err)
	}
	return fmt.Errorf("所有 PAT 都无法执行 %s: %w", action, lastErr)
}

// Rebuild executes the full rebuild orchestration:
// shutdown old → network_helper on → delete → create (fresh ed25519 key,
// booted) → wait running → network_helper off + shutdown → wait offline →
// assign reserved IP (optional) → boot → wait running → sshrun script →
// probe SSH port.
func (e *Executor) Rebuild(ctx context.Context, id int, script, reservedIP, pat string) (string, error) {
	api := e.API(pat)
	step := func(n int, format string, args ...any) {
		label := fmt.Sprintf("step %d: %s", n, fmt.Sprintf(format, args...))
		if e.Progress != nil {
			e.Progress(label)
		}
		if e.Log != nil {
			e.Log.Info(label)
		}
	}

	step(0, "读取旧实例 %d", id)
	old, err := api.GetInstance(ctx, id)
	if err != nil {
		return "", fmt.Errorf("读取旧实例失败: %w", err)
	}

	step(1, "关机旧实例 %d", id)
	if err := api.Shutdown(ctx, id); err != nil {
		return "", fmt.Errorf("关机旧实例失败: %w", err)
	}

	// network_helper must be on before deletion so the new instance inherits it.
	// Failure is best-effort (networking may work via DHCP anyway).
	step(2, "启用 network_helper (旧实例 %d)", id)
	if cfgID, cfgErr := api.GetConfigID(ctx, id); cfgErr == nil {
		if err := api.UpdateConfigNetworkHelper(ctx, id, cfgID, true); err != nil {
			if e.Log != nil {
				e.Log.Warn("network_helper 启用失败（继续）", "err", err)
			}
		}
	} else {
		if e.Log != nil {
			e.Log.Warn("获取配置失败（继续）", "err", cfgErr)
		}
	}

	step(3, "删除旧实例 %d", id)
	if err := api.DeleteInstance(ctx, id); err != nil {
		return "", fmt.Errorf("删除旧实例失败: %w", err)
	}

	privPEM, pubKey, err := sshrun.GenerateKey()
	if err != nil {
		return "", fmt.Errorf("生成 SSH 密钥失败: %w", err)
	}
	booted := true
	step(4, "创建新实例 (region=%s type=%s image=%s)", old.Region, old.Type, old.Image)
	newInst, err := api.CreateInstance(ctx, linode.CreateOptions{
		Region:         old.Region,
		Type:           old.Type,
		Image:          old.Image,
		AuthorizedKeys: []string{pubKey},
		Booted:         &booted,
	})
	if err != nil {
		return "", fmt.Errorf("创建新实例失败: %w", err)
	}
	newID := newInst.ID

	step(5, "等待新实例 %d 运行", newID)
	if err := e.waitStatus(ctx, api, newID, "running"); err != nil {
		return "", err
	}

	step(6, "关闭 network_helper 并关机 (新实例 %d)", newID)
	if cfgID, cfgErr := api.GetConfigID(ctx, newID); cfgErr == nil {
		if err := api.UpdateConfigNetworkHelper(ctx, newID, cfgID, false); err != nil {
			if e.Log != nil {
				e.Log.Warn("network_helper 关闭失败（继续）", "err", err)
			}
		}
	} else {
		if e.Log != nil {
			e.Log.Warn("获取配置失败（继续）", "err", cfgErr)
		}
	}
	if err := api.Shutdown(ctx, newID); err != nil {
		return "", fmt.Errorf("关机新实例失败: %w", err)
	}
	step(6, "等待新实例 %d 离线", newID)
	if err := e.waitStatus(ctx, api, newID, "offline"); err != nil {
		return "", err
	}

	// Default-open-all-ports: attach the allow-all Cloud Firewall while the
	// instance is offline (config changes apply at the next boot). Best-effort.
	step(6, "绑定开放全部端口防火墙 (新实例 %d)", newID)
	if err := api.AttachAllowAllFirewall(ctx, newInst.Region, newID); err != nil {
		if e.Log != nil {
			e.Log.Warn("防火墙绑定失败（继续）", "err", err)
		}
	}

	if reservedIP != "" {
		step(7, "分配保留 IP %s", reservedIP)
		if err := api.AssignIP(ctx, old.Region, reservedIP, newID); err != nil {
			return "", fmt.Errorf("分配保留 IP 失败: %w", err)
		}
	}

	step(8, "开机新实例 %d", newID)
	if err := api.Boot(ctx, newID); err != nil {
		return "", fmt.Errorf("开机失败: %w", err)
	}
	step(8, "等待新实例 %d 运行", newID)
	if err := e.waitStatus(ctx, api, newID, "running"); err != nil {
		return "", err
	}

	step(9, "获取新实例 IP 并执行开机脚本")
	ips, err := api.GetInstanceIPs(ctx, newID)
	if err != nil {
		return "", fmt.Errorf("获取新实例 IP 失败: %w", err)
	}
	host := ips.PublicIPv4()
	if host == "" {
		host = ips.SLAACv6()
	}
	if host == "" {
		return "", fmt.Errorf("新实例没有可用的公网 IP")
	}
	if e.Run == nil {
		return "", fmt.Errorf("SSH 运行器未配置")
	}
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := e.Run(runCtx, host, e.SSHPort, privPEM, script); err != nil {
		return "", fmt.Errorf("SSH 执行开机脚本失败: %w", err)
	}

	step(10, "探测 SSH 端口 %s:%d", host, e.SSHPort)
	if err := e.ProbePort(ctx, host); err != nil {
		return "", fmt.Errorf("SSH 端口探测失败: %w", err)
	}
	return host, nil
}

// WaitStatus polls the instance until it reaches want or the timeout elapses.
func (e *Executor) WaitStatus(ctx context.Context, api LinodeAPI, id int, want string) error {
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

// waitStatus is the internal helper used by Rebuild.
func (e *Executor) waitStatus(ctx context.Context, api LinodeAPI, id int, want string) error {
	return e.WaitStatus(ctx, api, id, want)
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
