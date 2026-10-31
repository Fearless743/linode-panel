// Package api exposes the HTTP JSON API for the linode-panel web UI.
//
// Routes and JSON shapes are a frozen contract shared with the frontend
// (web/) and the Linode client (internal/linode). See the package README
// or the top-level README for the exact shapes.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"linode-panel/internal/agenthub"
	"linode-panel/internal/linode"
	"linode-panel/internal/store"
)

// LinodeAPI is the subset of the Linode client used by the HTTP handlers.
// It is satisfied by *linode.Client and is the seam used by tests.
type LinodeAPI interface {
	ListInstances(ctx context.Context) ([]linode.Instance, error)
	GetInstance(ctx context.Context, id int) (linode.Instance, error)
	Boot(ctx context.Context, id int) error
	Shutdown(ctx context.Context, id int) error
	Reboot(ctx context.Context, id int) error
	GetInstanceIPs(ctx context.Context, id int) (linode.InstanceIPs, error)
	ListSSHKeys(ctx context.Context) ([]linode.SSHKey, error)
	CreateInstance(ctx context.Context, opts linode.CreateOptions) (linode.Instance, error)
	DeleteInstance(ctx context.Context, id int) error
	GetConfigID(ctx context.Context, id int) (int, error)
	UpdateConfigNetworkHelper(ctx context.Context, id, configID int, enabled bool) error
	AssignIP(ctx context.Context, region, address string, linodeID int) error
	// AttachAllowAllFirewall ensures the account's allow-all Cloud Firewall is
	// attached to the Linode (idempotent). Best-effort at call sites.
	AttachAllowAllFirewall(ctx context.Context, region string, linodeID int) error
}

// Server serves the panel's JSON API.
type Server struct {
	API     LinodeAPI
	SSHPort int // 0 is treated as 22

	// Store persists users, groups, tokens, and sessions. It is initialized
	// by New and is required for all auth-gated routes.
	Store *store.Store

	// APIBase is the Linode API v4 base URL used by per-PAT clients.
	// Defaults to the official endpoint when empty.
	APIBase string

	// Tasks serializes rebuild jobs. It is initialized by New.
	Tasks *Manager

	// Hub tracks registered agents and their commands. It is initialized by
	// New; agent registration uses the store's RegCode.
	Hub *agenthub.Hub

	// localAgentID is the id of the panel's own self-agent (0 when disabled).
	// The panel registers itself as an agent so via_agent operations can run
	// in-process; see EnableSelfAgent.
	localAgentID int

	// localAgentKey seals commands routed to the panel's own self-agent. It is
	// set by EnableSelfAgent: derived from the registration code when one is
	// set, otherwise a random per-run key so the panel can act as an agent
	// without configuring a registration code.
	localAgentKey []byte

	// AutoRebuildCheckAPI overrides the China-side probe URL used by the
	// auto-rebuild monitor (empty = DefaultBlockCheckAPI).
	AutoRebuildCheckAPI string

	// monitor is the running auto-rebuild background monitor (nil until
	// StartAutoRebuildMonitor is called). Handlers use it for on-demand checks.
	monitor *AutoRebuildMonitor

	clientMu sync.Mutex
	clients  map[string]*linode.Client
}

// New returns a Server backed by api, using st for users/groups/PATs and
// sessions. The api is kept as the fallback LinodeAPI used by the rebuild
// Manager; per-request operation routes use clients built from each user's
// PAT via clientFor.
func New(api LinodeAPI, st *store.Store) *Server {
	s := &Server{
		API:     api,
		Store:   st,
		Hub:     agenthub.NewHub(),
		clients: make(map[string]*linode.Client),
	}
	s.Tasks = NewManager(api)
	return s
}

// clientFor returns a cached Linode client for pat, configured with the
// server's APIBase (defaulting to the official endpoint).
func (s *Server) clientFor(pat string) *linode.Client {
	base := s.APIBase
	if base == "" {
		base = "https://api.linode.com/v4"
	}
	s.clientMu.Lock()
	defer s.clientMu.Unlock()
	if c, ok := s.clients[pat]; ok {
		return c
	}
	c := linode.New(pat)
	c.BaseURL = base
	s.clients[pat] = c
	return c
}

// Handler returns the route mux. Only /api/... routes are registered;
// the caller is expected to mount it at /api/.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// Agent registration/poll/result are public (agent token gates poll/result).
	mux.HandleFunc("POST /api/agents/register", s.handleAgentRegister)
	mux.HandleFunc("POST /api/agent/poll", s.handleAgentPoll)
	mux.HandleFunc("POST /api/agent/result", s.handleAgentResult)

	// Auth routes are public.
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/auth/me", s.handleMe)

	// 账号管理：任何登录用户管理自己的 PAT；创建实例需 power 权限。
	mux.HandleFunc("GET /api/account/tokens", s.requireAuth("", s.handleListMyTokens))
	mux.HandleFunc("POST /api/account/tokens", s.requireAuth("", s.handleAddMyToken))
	mux.HandleFunc("DELETE /api/account/tokens/{tid}", s.requireAuth("", s.handleDeleteMyToken))
	mux.HandleFunc("PUT /api/account/tokens/{tid}/agent", s.requireAuth("", s.handleSetTokenAgent))
	mux.HandleFunc("GET /api/account/agents", s.requireAuth("", s.handleAccountAgents))
	mux.HandleFunc("GET /api/account/options", s.requireAuth(store.PermPower, s.handleAccountOptions))
	mux.HandleFunc("POST /api/account/instances", s.requireAuth(store.PermPower, s.handleCreateInstance))

	// 账号组：任何登录用户管理自己的账号组与成员。
	mux.HandleFunc("GET /api/account/groups", s.requireAuth("", s.handleListAccountGroups))
	mux.HandleFunc("POST /api/account/groups", s.requireAuth("", s.handleCreateAccountGroup))
	mux.HandleFunc("PUT /api/account/groups/{gid}", s.requireAuth("", s.handleUpdateAccountGroup))
	mux.HandleFunc("DELETE /api/account/groups/{gid}", s.requireAuth("", s.handleDeleteAccountGroup))
	mux.HandleFunc("POST /api/account/groups/{gid}/tokens", s.requireAuth("", s.handleAddAccountGroupMember))
	mux.HandleFunc("DELETE /api/account/groups/{gid}/tokens/{tid}", s.requireAuth("", s.handleRemoveAccountGroupMember))
	mux.HandleFunc("GET /api/account/groups/{gid}/instance-config", s.requireAuth("", s.handleGetAccountGroupInstanceConfig))
	mux.HandleFunc("PUT /api/account/groups/{gid}/instance-config", s.requireAuth("", s.handlePutAccountGroupInstanceConfig))

	// 账号组自动替换：组内某账号封号/无余额时换到另一账号。
	mux.HandleFunc("GET /api/account/failover", s.requireAuth("", s.handleListFailover))
	mux.HandleFunc("PUT /api/account/failover/{iid}", s.requireAuth("", s.handlePutFailover))
	mux.HandleFunc("DELETE /api/account/failover/{iid}", s.requireAuth("", s.handleDeleteFailover))

	// View permission.
	mux.HandleFunc("GET /api/instances", s.requireAuth(store.PermView, s.handleListInstances))
	mux.HandleFunc("GET /api/instances/{id}/refresh", s.requireAuth(store.PermView, s.handleInstanceRefresh))
	mux.HandleFunc("GET /api/instances/{id}/status", s.requireAuth(store.PermView, s.handleInstanceStatus))
	mux.HandleFunc("GET /api/instances/{id}/script", s.requireAuth(store.PermScript, s.handleGetInstanceScript))
	mux.HandleFunc("PUT /api/instances/{id}/script", s.requireAuth(store.PermScript, s.handlePutInstanceScript))
	mux.HandleFunc("DELETE /api/instances/{id}/script", s.requireAuth(store.PermScript, s.handleDeleteInstanceScript))
	mux.HandleFunc("GET /api/instance-scripts", s.requireAuth(store.PermScript, s.handleInstanceScriptStatus))
	mux.HandleFunc("GET /api/ssh-keys", s.requireAuth(store.PermView, s.handleListSSHKeys))

	// Power permission.
	mux.HandleFunc("POST /api/instances/{id}/boot", s.requireAuth(store.PermPower, s.handleBoot))
	mux.HandleFunc("POST /api/instances/{id}/shutdown", s.requireAuth(store.PermPower, s.handleShutdown))
	mux.HandleFunc("POST /api/instances/{id}/reboot", s.requireAuth(store.PermPower, s.handleReboot))

	// Rebuild permission.
	mux.HandleFunc("POST /api/instances/{id}/rebuild", s.requireAuth(store.PermRebuild, s.handleRebuild))
	mux.HandleFunc("GET /api/rebuild/tasks", s.requireAuth(store.PermRebuild, s.handleRebuildTasks))
	mux.HandleFunc("GET /api/rebuild/tasks/{id}", s.requireAuth(store.PermRebuild, s.handleRebuildTask))

	// 被墙后自动重建换 IP（按实例配置）。
	mux.HandleFunc("GET /api/instances/{id}/auto-rebuild", s.requireAuth(store.PermRebuild, s.handleGetAutoRebuild))
	mux.HandleFunc("PUT /api/instances/{id}/auto-rebuild", s.requireAuth(store.PermRebuild, s.handlePutAutoRebuild))
	mux.HandleFunc("DELETE /api/instances/{id}/auto-rebuild", s.requireAuth(store.PermRebuild, s.handleDeleteAutoRebuild))
	mux.HandleFunc("POST /api/instances/{id}/auto-rebuild/check", s.requireAuth(store.PermRebuild, s.handleCheckAutoRebuild))
	mux.HandleFunc("GET /api/auto-rebuild", s.requireAuth(store.PermRebuild, s.handleAutoRebuildList))

	// Admin permission (users, groups & agents).
	mux.HandleFunc("GET /api/users", s.requireAuth(store.PermAdmin, s.handleListUsers))
	mux.HandleFunc("POST /api/users", s.requireAuth(store.PermAdmin, s.handleCreateUser))
	mux.HandleFunc("PATCH /api/users/{id}", s.requireAuth(store.PermAdmin, s.handleUpdateUser))
	mux.HandleFunc("DELETE /api/users/{id}", s.requireAuth(store.PermAdmin, s.handleDeleteUser))
	mux.HandleFunc("GET /api/users/{id}/tokens", s.requireAuth(store.PermAdmin, s.handleListTokens))
	mux.HandleFunc("POST /api/users/{id}/tokens", s.requireAuth(store.PermAdmin, s.handleAddToken))
	mux.HandleFunc("DELETE /api/users/{id}/tokens/{tid}", s.requireAuth(store.PermAdmin, s.handleDeleteToken))
	mux.HandleFunc("GET /api/groups", s.requireAuth(store.PermAdmin, s.handleListGroups))
	mux.HandleFunc("POST /api/groups", s.requireAuth(store.PermAdmin, s.handleCreateGroup))
	mux.HandleFunc("PUT /api/groups/{id}", s.requireAuth(store.PermAdmin, s.handleUpdateGroup))
	mux.HandleFunc("DELETE /api/groups/{id}", s.requireAuth(store.PermAdmin, s.handleDeleteGroup))
	mux.HandleFunc("GET /api/agents", s.requireAuth(store.PermAdmin, s.handleListAgents))
	mux.HandleFunc("POST /api/agents", s.requireAuth(store.PermAdmin, s.handleCreateAgent))
	mux.HandleFunc("DELETE /api/agents/{id}", s.requireAuth(store.PermAdmin, s.handleDeleteAgent))
	mux.HandleFunc("PUT /api/agents/{id}/name", s.requireAuth(store.PermAdmin, s.handleRenameAgent))
	mux.HandleFunc("GET /api/agent/commands", s.requireAuth(store.PermAdmin, s.handleAgentCommands))
	mux.HandleFunc("POST /api/agents/regcode", s.requireAuth(store.PermAdmin, s.handleSetRegCode))
	return mux
}

// sshPort normalizes SSHPort==0 to 22.
func (s *Server) sshPort() int {
	if s.SSHPort == 0 {
		return 22
	}
	return s.SSHPort
}

// sshCommand renders the `ssh` command for a host. The user is fixed to
// root; a non-22 port is rendered with -p.
func (s *Server) sshCommand(host string) string {
	if host == "" {
		return ""
	}
	if port := s.sshPort(); port != 22 {
		return fmt.Sprintf("ssh -p %d root@%s", port, host)
	}
	return "ssh root@" + host
}

// listHost picks the host used for the list ssh_command: first public IPv4,
// then SLAAC IPv6.
func (s *Server) listHost(in linode.Instance) string {
	if len(in.IPv4) > 0 {
		return in.IPv4[0]
	}
	return in.IPv6
}

// statusHost picks the host used for the status ssh_command / probe.
func (s *Server) statusHost(ips linode.InstanceIPs) string {
	if h := ips.PublicIPv4(); h != "" {
		return h
	}
	return ips.SLAACv6()
}

func (s *Server) handleListInstances(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	// 只有手动刷新（?refresh=1）才强制请求 Linode API；否则优先返回
	// 数据库中的缓存快照（首次没有缓存时仍需拉取）。
	forceRefresh := r.URL.Query().Get("refresh") == "1"
	resp := listInstancesResponse{Instances: []instanceJSON{}}
	for _, tok := range s.Store.TokensOf(user.ID) {
		client := s.clientFor(tok.Token)

		cached := s.Store.CachedInstances(tok.ID)
		if !forceRefresh && len(cached) > 0 {
			s.appendInstances(&resp, cached, tok, "")
			continue
		}

		insts, err := client.ListInstances(r.Context())
		if err != nil {
			// A single failed PAT must not break the whole list; fall back to
			// the last cached snapshot when one exists.
			slog.Warn("list instances failed", "token_id", tok.ID, "error", err)
			if len(cached) > 0 {
				s.appendInstances(&resp, cached, tok, "Linode API 暂不可用，显示缓存数据")
			}
			continue
		}
		// Persist the freshly fetched snapshot for future reads.
		if err := s.Store.CacheInstances(tok.ID, insts); err != nil {
			slog.Warn("cache instances failed", "token_id", tok.ID, "error", err)
		}
		s.appendInstances(&resp, insts, tok, "")
	}
	writeJSON(w, http.StatusOK, resp)
}

// appendInstances converts linode instances into the response shape and
// appends them to resp, tagging each with fetchErr when served from cache.
func (s *Server) appendInstances(resp *listInstancesResponse, insts []linode.Instance, tok store.Token, fetchErr string) {
	for _, in := range insts {
		resp.Instances = append(resp.Instances, instanceJSON{
			ID:         in.ID,
			Label:      in.Label,
			Status:     in.Status,
			Region:     in.Region,
			Type:       in.Type,
			Image:      in.Image,
			IPv4:       orEmpty(in.IPv4),
			IPv6:       in.IPv6,
			SSHCommand: s.sshCommand(s.listHost(in)),
			Tags:       orEmpty(in.Tags),
			Created:    in.Created,
			TokenID:    tok.ID,
			TokenLabel: tok.Label,
			FetchError: fetchErr,
		})
	}
}

// handleInstanceRefresh re-fetches one instance from the Linode API, updates
// its cached snapshot, and returns the full row. It backs the per-row
// refresh button, so a manual refresh of a single instance goes through the
// API instead of the cache.
func (s *Server) handleInstanceRefresh(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	user, _ := UserFrom(r.Context())
	tokenID := r.URL.Query().Get("token_id")
	pat, err := s.resolveToken(user, tokenID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	inst, err := s.clientFor(pat).GetInstance(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	if tid := s.tokenIDFor(user, tokenID); tid > 0 {
		if err := s.Store.UpsertInstanceCache(tid, inst); err != nil {
			slog.Warn("upsert instance cache failed", "token_id", tid, "instance_id", id, "error", err)
		}
	}
	writeJSON(w, http.StatusOK, instanceJSON{
		ID:         inst.ID,
		Label:      inst.Label,
		Status:     inst.Status,
		Region:     inst.Region,
		Type:       inst.Type,
		Image:      inst.Image,
		IPv4:       orEmpty(inst.IPv4),
		IPv6:       inst.IPv6,
		SSHCommand: s.sshCommand(s.listHost(inst)),
		Tags:       orEmpty(inst.Tags),
		Created:    inst.Created,
		TokenID:    s.tokenIDFor(user, tokenID),
		TokenLabel: s.tokenLabel(user, tokenID),
	})
}

func (s *Server) handleInstanceStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	client, ok := s.operationClient(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	inst, err := client.GetInstance(ctx, id)
	if err != nil {
		writeError(w, err)
		return
	}
	ips, err := client.GetInstanceIPs(ctx, id)
	if err != nil {
		writeError(w, err)
		return
	}

	host := s.statusHost(ips)
	sshReady := false
	sshErr := ""
	if inst.Status == "running" && host != "" {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(s.sshPort())), 3*time.Second)
		if err != nil {
			sshErr = err.Error()
		} else {
			_ = conn.Close()
			sshReady = true
		}
	}

	writeJSON(w, http.StatusOK, statusResponse{
		ID:         inst.ID,
		Label:      inst.Label,
		Status:     inst.Status,
		IPv4:       orEmpty(inst.IPv4),
		IPv6:       inst.IPv6,
		SSHCommand: s.sshCommand(host),
		SSHReady:   sshReady,
		SSHError:   sshErr,
	})
}

func (s *Server) handleListSSHKeys(w http.ResponseWriter, r *http.Request) {
	client, ok := s.operationClient(w, r)
	if !ok {
		return
	}
	keys, err := client.ListSSHKeys(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	resp := listSSHKeysResponse{Keys: make([]sshKeyJSON, 0, len(keys))}
	for _, k := range keys {
		resp.Keys = append(resp.Keys, sshKeyJSON{
			ID:      k.ID,
			Label:   k.Label,
			Key:     k.SSHKey,
			Created: k.Created,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// operationClient resolves the request's token_id to a per-PAT Linode client.
// An empty token_id selects the user's first PAT; a missing/invalid token
// yields a 400. Callers must check ok before using the client.
func (s *Server) operationClient(w http.ResponseWriter, r *http.Request) (*linode.Client, bool) {
	user, _ := UserFrom(r.Context())
	pat, err := s.resolveToken(user, r.URL.Query().Get("token_id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return nil, false
	}
	return s.clientFor(pat), true
}

// handleBoot/handleShutdown/handleReboot perform a power action with the
// request's token.
func (s *Server) handleBoot(w http.ResponseWriter, r *http.Request) {
	s.powerAction(w, r, agenthub.ActionBoot, func(c *linode.Client) func(context.Context, int) error {
		return c.Boot
	})
}

func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	s.powerAction(w, r, agenthub.ActionShutdown, func(c *linode.Client) func(context.Context, int) error {
		return c.Shutdown
	})
}

func (s *Server) handleReboot(w http.ResponseWriter, r *http.Request) {
	s.powerAction(w, r, agenthub.ActionReboot, func(c *linode.Client) func(context.Context, int) error {
		return c.Reboot
	})
}

func (s *Server) powerAction(w http.ResponseWriter, r *http.Request, action string, pick func(*linode.Client) func(context.Context, int) error) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	// Agent path: queue the command for a registered agent instead of calling
	// the Linode API directly. The PAT is sealed under the registration code.
	if r.URL.Query().Get("via_agent") == "1" {
		cmdID, ok := s.enqueueAgentCommand(w, r, agenthub.CmdPower, map[string]any{
			"id":     id,
			"action": action,
		})
		if !ok {
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"command_id": cmdID})
		return
	}
	client, ok := s.operationClient(w, r)
	if !ok {
		return
	}
	if err := pick(client)(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	// Drop the token's cached snapshot so the next list refetches and shows
	// the new power state instead of the stale cached one.
	user, _ := UserFrom(r.Context())
	if tid := s.tokenIDFor(user, r.URL.Query().Get("token_id")); tid > 0 {
		if err := s.Store.InvalidateInstanceCache(tid); err != nil {
			slog.Warn("invalidate instance cache failed", "token_id", tid, "error", err)
		}
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

func parseID(w http.ResponseWriter, r *http.Request) (int, bool) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "无效的实例 ID"})
		return 0, false
	}
	return id, true
}

// writeError maps errors to Chinese JSON messages per the shared contract.
func writeError(w http.ResponseWriter, err error) {
	var apiErr *linode.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "Linode Token 无效或权限不足"})
		case http.StatusNotFound:
			writeJSON(w, http.StatusNotFound, errorResponse{Error: "实例不存在"})
		default:
			writeJSON(w, http.StatusBadGateway, errorResponse{Error: "Linode API 错误: " + apiErr.Reason})
		}
		return
	}
	writeJSON(w, http.StatusBadGateway, errorResponse{Error: "上游请求失败: " + err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// JSON response shapes (field names are frozen by the shared contract).
type listInstancesResponse struct {
	Instances []instanceJSON `json:"instances"`
}

type instanceJSON struct {
	ID         int      `json:"id"`
	Label      string   `json:"label"`
	Status     string   `json:"status"`
	Region     string   `json:"region"`
	Type       string   `json:"type"`
	Image      string   `json:"image"`
	IPv4       []string `json:"ipv4"`
	IPv6       string   `json:"ipv6"`
	SSHCommand string   `json:"ssh_command"`
	Tags       []string `json:"tags"`
	Created    string   `json:"created"`
	TokenID    int      `json:"token_id"`
	TokenLabel string   `json:"token_label"`
	FetchError string   `json:"fetch_error,omitempty"`
}

type statusResponse struct {
	ID         int      `json:"id"`
	Label      string   `json:"label"`
	Status     string   `json:"status"`
	IPv4       []string `json:"ipv4"`
	IPv6       string   `json:"ipv6"`
	SSHCommand string   `json:"ssh_command"`
	SSHReady   bool     `json:"ssh_ready"`
	SSHError   string   `json:"ssh_error"`
}

type listSSHKeysResponse struct {
	Keys []sshKeyJSON `json:"keys"`
}

type sshKeyJSON struct {
	ID      int    `json:"id"`
	Label   string `json:"label"`
	Key     string `json:"key"`
	Created string `json:"created"`
}

type okResponse struct {
	OK bool `json:"ok"`
}

type errorResponse struct {
	Error string `json:"error"`
}
