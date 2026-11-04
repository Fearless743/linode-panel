// Command linode-panel is a small web panel to list and power-cycle
// Linode instances. It proxies the Linode API v4 behind a JSON API and
// serves a static web UI. Users log in and their Personal Access Tokens
// are read from the SQLite database; tokens are never sent to the browser.
package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"linode-panel/internal/api"
	"linode-panel/internal/linode"
	"linode-panel/internal/store"
)

//go:embed web
var webFS embed.FS

func main() {
	addr := flag.String("addr", "127.0.0.1:8787", "HTTP 监听地址")
	apiBase := flag.String("api-base", defaultAPIPrefix(), "Linode API v4 基础地址")
	sshPort := flag.Int("ssh-port", 22, "SSH 端口（用于生成的 ssh 命令与连通性探测）")
	state := flag.String("state", defaultStatePath(), "SQLite 数据库路径（保存用户、PAT、实例开机脚本）")
	secretKey := flag.String("secret-key", "", "数据库敏感字段加密主密钥（留空则读取 LINODE_PANEL_SECRET_KEY，否则使用数据库旁的 .key 文件）")
	agentCode := flag.String("agent-code", "", "初始 agent 注册码（可选；若数据库已有注册码则优先使用）")
	selfAgent := flag.Bool("self-agent", true, "面板自身也作为 agent：注册一个本地面板 agent，使经 agent 操作可由面板进程直接执行")
	selfAgentName := flag.String("self-agent-name", "本地面板", "本地面板 agent 的名称（配合 --self-agent 使用；后台重命名后默认沿用持久化的名称）")
	autoRebuild := flag.Bool("auto-rebuild", true, "启用「被墙后自动删机重建换 IP」后台监测")
	autoRebuildTick := flag.Duration("auto-rebuild-tick", time.Minute, "自动换 IP 监听的轮询间隔（每个实例仍按自身配置的检测间隔探测）")
	autoRebuildCheckAPI := flag.String("auto-rebuild-check-api", defaultAutoRebuildCheckAPI(), "国产 IP 连通性探测接口（被墙判定用，环境变量 AUTO_REBUILD_CHECK_API）")
	failoverEnabled := flag.Bool("failover", true, "启用账号组自动替换监测（组内账号封号/无余额时换到另一账号）")
	failoverTick := flag.Duration("failover-tick", time.Minute, "账号组自动替换监听的轮询间隔（每个实例仍按自身配置的检测间隔检查）")
	dualEnabled := flag.Bool("dual", true, "启用双机轮询替换监测（被墙时切换到备用机并重建）")
	dualTick := flag.Duration("dual-tick", time.Minute, "双机轮询监听的轮询间隔（每个账号仍按自身配置的检测间隔探测）")
	cacheEnabled := flag.Bool("instance-cache", true, "启用实例缓存后台刷新（每轮拉取所有账号实例并更新列表缓存）")
	cacheTick := flag.Duration("instance-cache-tick", time.Minute, "实例缓存后台刷新的轮询间隔")
	tgBotToken := flag.String("tg-bot-token", defaultTGBotToken(), "Telegram Bot Token（为空则用数据库配置，环境变量 TG_BOT_TOKEN）")
	tgChatID := flag.String("tg-chat-id", defaultTGChatID(), "Telegram Chat ID（为空则用数据库配置，环境变量 TG_CHAT_ID）")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	log := logger

	st, adminPass, err := store.OpenWithKey(*state, *secretKey)
	if err != nil {
		log.Error("打开数据库失败", "error", err)
		os.Exit(1)
	}
	if adminPass != "" {
		log.Info(fmt.Sprintf("初始管理员账号：admin / 密码：%s（请尽快登录修改）", adminPass))
	}
	// The persisted registration code wins over --agent-code; the flag only
	// seeds a fresh store.
	if *agentCode != "" && st.RegCode() == "" {
		if err := st.SetRegCode(*agentCode); err != nil {
			log.Error("设置注册码失败", "error", err)
			os.Exit(1)
		}
	}
	// TG 通知凭证：命令行/环境变量优先种子数据库（两者齐全时自动开启）。
	if *tgBotToken != "" || *tgChatID != "" {
		cur := st.GetNotifyConfig()
		token := cur.HasToken
		chat := cur.ChatID
		newChat := chat
		if *tgChatID != "" {
			newChat = *tgChatID
		}
		newToken := ""
		if *tgBotToken != "" {
			newToken = *tgBotToken
		}
		enable := cur.Enabled || (newChat != "" && (token || newToken != ""))
		if err := st.SetNotifyConfig(enable, newChat, newToken); err != nil {
			log.Error("设置 TG 通知失败", "error", err)
			os.Exit(1)
		}
	}

	client := linode.New("")

	apiSrv := api.New(client, st)
	apiSrv.SSHPort = *sshPort
	apiSrv.APIBase = *apiBase
	apiSrv.AutoRebuildCheckAPI = *autoRebuildCheckAPI
	apiSrv.Tasks.APIBase = *apiBase
	apiSrv.Tasks.SetSSHPort(*sshPort)

	// 面板自身作为 agent：注册一个本地面板 agent，经 agent 操作可在面板进程内执行。
	// 该 agent 与远程 agent 一样出现在 Agent 列表中，可被账号绑定。
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	// 被墙后自动删机重建换 IP 的后台监测。
	if *autoRebuild {
		apiSrv.StartAutoRebuildMonitor(rootCtx, *autoRebuildTick)
		log.Info("自动换 IP 监测已启动", "tick", autoRebuildTick.String(), "check_api", *autoRebuildCheckAPI)
	}

	// 账号组自动替换：组内账号封号/无余额时换到同组另一账号的后台监测。
	if *failoverEnabled {
		apiSrv.StartFailoverMonitor(rootCtx, *failoverTick)
		log.Info("账号组自动替换监测已启动", "tick", failoverTick.String())
	}

	// 双机轮询替换：被墙时切换到备用机并重建旧机。
	if *dualEnabled {
		apiSrv.StartDualMonitor(rootCtx, *dualTick)
		log.Info("双机轮询监测已启动", "tick", dualTick.String())
	}

	// 实例缓存后台刷新：每轮拉取所有账号实例并更新列表缓存。
	if *cacheEnabled {
		apiSrv.StartInstanceCacheMonitor(rootCtx, *cacheTick)
		log.Info("实例缓存刷新已启动", "tick", cacheTick.String())
	}

	if *selfAgent {
		// 名称优先级：命令行显式指定的 --self-agent-name > 后台重命名
		// 持久化的名称 > 默认「本地面板」。
		name := *selfAgentName
		if !flagPassed("self-agent-name") {
			if saved := st.SelfAgentName(); saved != "" {
				name = saved
			}
		}
		id := apiSrv.EnableSelfAgent(rootCtx, name)
		log.Info("面板已注册为本地 agent", "agent_id", id, "name", name)
	}

	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Error("解析内嵌静态资源失败", "error", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.Handle("/api/", apiSrv.Handler())
	mux.Handle("/", spaHandler(sub))

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("linode-panel 启动", "addr", *addr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		log.Error("HTTP 服务异常退出", "error", err)
		os.Exit(1)
	case sig := <-stop:
		log.Info("收到信号，开始优雅关闭", "signal", sig.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(ctx); err != nil {
		log.Error("优雅关闭失败", "error", err)
		os.Exit(1)
	}
	log.Info("已退出")
}

// spaHandler serves the embedded static site, falling back to index.html for
// client-side routes (React Router). /api/ is routed by the mux above and
// never reaches this handler.
func spaHandler(sub fs.FS) http.Handler {
	fileServer := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path != "" {
			if info, err := fs.Stat(sub, path); err == nil && !info.IsDir() {
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		// Unknown path: serve the SPA entry for client-side routing.
		http.ServeFileFS(w, r, sub, "index.html")
	})
}

// flagPassed reports whether the named flag was set on the command line.
func flagPassed(name string) bool {
	found := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

func defaultAPIPrefix() string {
	if v := os.Getenv("LINODE_API_BASE"); v != "" {
		return v
	}
	return "https://api.linode.com/v4"
}

// defaultAutoRebuildCheckAPI returns the China-side reachability probe used by
// the auto-rebuild monitor, overridable via AUTO_REBUILD_CHECK_API.
func defaultAutoRebuildCheckAPI() string {
	if v := strings.TrimSpace(os.Getenv("AUTO_REBUILD_CHECK_API")); v != "" {
		return v
	}
	return api.DefaultBlockCheckAPI
}

func defaultTGBotToken() string {
	return strings.TrimSpace(os.Getenv("TG_BOT_TOKEN"))
}

func defaultTGChatID() string {
	return strings.TrimSpace(os.Getenv("TG_CHAT_ID"))
}

// defaultStatePath returns the default SQLite database location under the
// ./db directory of the current working directory.
func defaultStatePath() string {
	return filepath.Join("db", "state.db")
}
