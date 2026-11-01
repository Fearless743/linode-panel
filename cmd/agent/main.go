// Command agent runs on a remote host and executes Linode operations
// dispatched by the linode-panel hub. It registers once with the panel using
// the shared registration code, then long-polls for commands, runs them
// locally against the Linode API v4, and reports results back. Each command
// carries the Linode PAT it needs; the agent never persists it.
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

	"linode-panel/internal/agenthub"
)

func main() {
	panel := flag.String("panel", "", "面板 base URL（必填）")
	code := flag.String("code", "", "注册码（首次注册用；已有 token 时可省略）")
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
	pc := newPanelClient(*panel, st.AgentToken)

	if st.AgentToken == "" {
		if *code == "" {
			fatal("缺少注册码：首次运行必须用 --code 注册 agent")
		}
		if err := ensureToken(context.Background(), &st, *statePath, pc, *code, hostname); err != nil {
			fatal("%v", err)
		}
		log.Info("已注册 agent", "agent_id", st.AgentID)
	}

	ex := &agenthub.Executor{
		APIBase: *apiBase,
		SSHPort: *sshPort,
		Log:     log,
	}
	a := &agent{
		pc:       pc,
		hostname: hostname,
		exec:     ex,
		log:      log,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	log.Info("agent 启动", "panel", *panel, "hostname", hostname)
	if err := a.run(ctx); err != nil {
		log.Error("agent 退出", "err", err)
		os.Exit(1)
	}
}

// fatal writes a Chinese error to stderr and exits non-zero.
func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "错误: "+format+"\n", args...)
	os.Exit(1)
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
