package main

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	"linode-panel/internal/linode"
	"linode-panel/internal/sshrun"
)

// rebuild executes the full rebuild orchestration on the agent host:
// shutdown old → network_helper on → delete → create (fresh ed25519 key,
// booted) → wait running → network_helper off + shutdown → wait offline →
// assign reserved IP (optional) → boot → wait running → sshrun script →
// probe SSH port.
//
// progress (when non-nil) is called before each step with a Chinese label.
func (e *executor) rebuild(ctx context.Context, id int, script, reservedIP, pat string) (host string, err error) {
	api := e.api(pat)
	step := func(n int, format string, args ...any) {
		label := fmt.Sprintf("step %d: %s", n, fmt.Sprintf(format, args...))
		if e.progress != nil {
			e.progress(label)
		}
		e.log.Info(label)
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

	// network_helper must be on before deletion so the new instance inherits
	// it. Failure is best-effort (networking may work via DHCP anyway).
	step(2, "启用 network_helper (旧实例 %d)", id)
	if cfgID, cfgErr := api.GetConfigID(ctx, id); cfgErr == nil {
		if err := api.UpdateConfigNetworkHelper(ctx, id, cfgID, true); err != nil {
			e.log.Warn("network_helper 启用失败（继续）", "err", err)
		}
	} else {
		e.log.Warn("获取配置失败（继续）", "err", cfgErr)
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
			e.log.Warn("network_helper 关闭失败（继续）", "err", err)
		}
	} else {
		e.log.Warn("获取配置失败（继续）", "err", cfgErr)
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
		e.log.Warn("防火墙绑定失败（继续）", "err", err)
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
	host = ips.PublicIPv4()
	if host == "" {
		host = ips.SLAACv6()
	}
	if host == "" {
		return "", fmt.Errorf("新实例没有可用的公网 IP")
	}
	if e.run == nil {
		return "", fmt.Errorf("SSH 运行器未配置")
	}
	runCtx, cancel := context.WithTimeout(ctx, sshExecTimeout)
	defer cancel()
	if err := e.run(runCtx, host, e.sshPort, privPEM, script); err != nil {
		return "", fmt.Errorf("SSH 执行开机脚本失败: %w", err)
	}

	step(10, "探测 SSH 端口 %s:%d", host, e.sshPort)
	if err := e.probePort(ctx, host); err != nil {
		return "", fmt.Errorf("SSH 端口探测失败: %w", err)
	}
	return host, nil
}

// waitStatus polls the instance until it reaches want or the timeout elapses.
func (e *executor) waitStatus(ctx context.Context, api linodeAPI, id int, want string) error {
	deadline := time.Now().Add(rebuildPollTimeout)
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
		case <-time.After(rebuildPollInterval):
		}
	}
}

// probePort attempts a TCP connection to host:sshPort, bounded by 3s.
func (e *executor) probePort(ctx context.Context, host string) error {
	if e.probe != nil {
		return e.probe(ctx, host, e.sshPort)
	}
	addr := net.JoinHostPort(host, strconv.Itoa(e.sshPort))
	d := net.Dialer{Timeout: 3 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}
