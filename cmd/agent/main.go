// Command agent runs on a remote host and executes Linode operations
// dispatched by the linode-panel hub. It polls the panel for commands
// (long-poll), runs them locally against the Linode API v4, and reports
// results back. The agent holds its own Linode PAT (injected by the panel via
// the set_pat command, or supplied through the state file) and never sends it
// to the panel.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"linode-panel/internal/agentcrypto"
	"linode-panel/internal/sshrun"
)

func main() {
	panel := flag.String("panel", "", "面板 base URL（必填）")
	code := flag.String("code", "", "注册码（首次注册用；已有 token 时可省略）")
	token := flag.String("token", "", "面板预签发的 agent token（免注册直连，需配合 --code 解密 PAT）")
	statePath := flag.String("state", defaultAgentStatePath(), "agent 本地状态文件路径")
	apiBase := flag.String("api-base", "https://api.linode.com/v4", "Linode API v4 基础地址")
	sshPort := flag.Int("ssh-port", 22, "SSH 端口（重建编排用）")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	log := logger

	if *panel == "" {
		fatal("必须提供 --panel（面板 base URL）")
	}

	hostname, err := os.Hostname()
	if err != nil {
		hostname = "agent"
	}

	st, err := loadState(*statePath)
	if err != nil {
		fatal("%v", err)
	}
	// A pre-issued panel token (--token) binds this process to the agent row
	// the operator pre-created in the panel, skipping registration. It is
	// persisted (with the registration code) so later restarts need neither
	// flag. A state file that already carries a token wins: this host has
	// already registered its own identity.
	if err := adoptToken(&st, *statePath, *token, *code); err != nil {
		fatal("%v", err)
	}
	// The registration code is needed to derive the PAT encryption key. On a
	// restart without --code, fall back to the code persisted at registration.
	if *code == "" && st.RegistrationCode != "" {
		*code = st.RegistrationCode
	}
	key := agentcrypto.DeriveKey(*code)
	pc := newPanelClient(*panel, st.AgentToken)

	if st.AgentToken == "" {
		if *code == "" {
			fatal("缺少注册码：首次运行必须用 --code 注册 agent（或使用面板预签发的 --token 直连）")
		}
		if err := ensureToken(context.Background(), &st, *statePath, pc, *code, hostname); err != nil {
			fatal("%v", err)
		}
		log.Info("已注册 agent", "agent_id", st.AgentID)
	}

	ex := &executor{
		apiBase: *apiBase,
		sshPort: *sshPort,
		run:     defaultRunFn,
		log:     log,
	}
	a := &agent{
		pc:        pc,
		st:        &st,
		statePath: *statePath,
		key:       key,
		code:      *code,
		hostname:  hostname,
		exec:      ex,
		log:       log,
	}
	if err := a.loadPATsFromState(); err != nil {
		log.Warn("加载本地 PAT 失败", "err", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	log.Info("agent 启动", "panel", *panel, "hostname", hostname)
	if err := a.run(ctx); err != nil {
		log.Error("agent 退出", "err", err)
		os.Exit(1)
	}
}

// adoptToken binds a pre-issued panel token (--token) into the agent state
// when the state file holds none, persisting it together with the
// registration code so later restarts need neither --token nor --code. A
// state file that already carries a token keeps it: that host has already
// registered its own identity.
func adoptToken(st *state, path, token, code string) error {
	if token == "" || st.AgentToken != "" {
		return nil
	}
	st.AgentToken = token
	if st.RegistrationCode == "" {
		st.RegistrationCode = code
	}
	if err := saveState(path, *st); err != nil {
		return fmt.Errorf("保存 agent token 失败: %w", err)
	}
	return nil
}

// defaultRunFn wraps sshrun.Run with the executor's signature.
func defaultRunFn(ctx context.Context, host string, port int, privPEM []byte, script string) error {
	res, err := sshrun.Run(ctx, sshrun.Options{
		Host:               host,
		Port:               port,
		PrivateKey:         privPEM,
		InsecureSkipVerify: true,
		Timeout:            sshExecTimeout,
	}, script)
	if err != nil {
		return err
	}
	if res.Exit != 0 {
		detail := res.Stderr
		if detail == "" {
			detail = res.Stdout
		}
		return &sshExitError{exit: res.Exit, detail: detail}
	}
	return nil
}

type sshExitError struct {
	exit   int
	detail string
}

func (e *sshExitError) Error() string {
	return "SSH 脚本退出码 " + itoa(e.exit) + ": " + e.detail
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// defaultAgentStatePath returns the agent state file under the user's config
// directory, falling back to the home directory.
func defaultAgentStatePath() string {
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		return filepath.Join(dir, "linode-panel-agent", "state.json")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".config", "linode-panel-agent", "state.json")
	}
	return "agent-state.json"
}
