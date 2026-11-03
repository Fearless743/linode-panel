package api

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"linode-panel/internal/linode"
	"linode-panel/internal/sshrun"
)

const (
	StatusRunning = "running"
	StatusSuccess = "success"
	StatusFailed  = "failed"
)

// DefaultBootScript is the built-in template used when an instance has no
// configured script.
const DefaultBootScript = `#!/usr/bin/env bash
# Linode 开机脚本（重建后自动执行）
set -euo pipefail
# 示例：更换 SSH 端口
# sed -i 's/^#Port 22/Port 2222/' /etc/ssh/sshd_config && systemctl restart sshd
`

// ErrNotFound is returned by Manager.Get for an unknown task id.
var ErrNotFound = errors.New("task not found")

// ErrBusy is returned by Manager.Start while a rebuild of the same instance is
// still running. Rebuilds of different instances may run concurrently.
var ErrBusy = errors.New("rebuild already in progress for this instance")

// Task is the observable state of one rebuild job.
type Task struct {
	ID         string   `json:"id"`
	InstanceID int      `json:"instance_id"`
	OldIP      string   `json:"old_ip"`
	NewIP      string   `json:"new_ip"`
	Status     string   `json:"status"`
	Err        string   `json:"error"`
	Script     string   `json:"script"`
	TokenLabel string   `json:"token_label"`
	Log        []string `json:"log"`
	StartedAt  string   `json:"started_at"`
	EndedAt    string   `json:"ended_at"`

	// rootPass is the instance's saved root password, used for the replacement
	// instance. It is deliberately unexported so it never appears in task JSON.
	rootPass string

	// NewInstanceID is the id of the instance created by the rebuild, set as
	// soon as it exists (InstanceID keeps the old, since-deleted id). 0 when
	// the rebuild failed before creating it.
	NewInstanceID int `json:"new_instance_id"`
}

// snapshot returns a copy with the log reversed (newest first) for JSON output.
func (t *Task) snapshot() Task {
	out := *t
	out.Log = make([]string, 0, len(t.Log))
	for i := len(t.Log) - 1; i >= 0; i-- {
		out.Log = append(out.Log, t.Log[i])
	}
	return out
}

// Manager serializes rebuild jobs per instance and stores their history.
// Different instances may be rebuilt concurrently.
type Manager struct {
	mu      sync.Mutex
	tasks   map[string]*Task
	order   []string       // insertion order, oldest first
	busy    map[int]string // instance id → running task id
	nextID  int
	sshPort int

	// api is the default Linode client used by handlers that do not carry a
	// per-request PAT (e.g. listing). Rebuild runs always create their own
	// per-PAT client via newAPI.
	api LinodeAPI

	// APIBase is the Linode API v4 base URL applied to per-PAT clients built
	// by newAPI; empty means the client default.
	APIBase string

	// newAPI builds a LinodeAPI for one PAT. Defaults to linode.New with
	// APIBase applied. Injectable for tests to record the per-run PAT.
	newAPI func(pat string) LinodeAPI

	// RunFn executes the boot script over SSH. Injectable for tests; defaults
	// to sshrun.Run.
	RunFn func(context.Context, sshrun.Options, string) (sshrun.Result, error)

	// ProbeFn checks TCP connectivity to a host's SSH port. Injectable for
	// tests; defaults to a net.DialTimeout with sshProbeTimeout.
	ProbeFn func(context.Context, string) error

	// PollInterval and PollTimeout bound status polling (steps 5-6).
	PollInterval time.Duration
	PollTimeout  time.Duration

	// BootScriptAttempts is the total number of SSH attempts when the boot
	// script cannot connect (sshd not listening yet), and
	// BootScriptRetryDelay is the pause between those attempts. Non-connect
	// errors are never retried. Defaults: 3 attempts, 1 minute apart.
	BootScriptAttempts   int
	BootScriptRetryDelay time.Duration

	// RepointCache, when set, is called as soon as a rebuild has created the
	// replacement instance: the panel re-points the instance's stable cache row
	// (and thus all per-instance configuration) at the new Linode id.
	RepointCache func(oldID, newID int)

	// ResolveInstance, when set, returns the panel's cached snapshot of an
	// instance by its Linode id. It is consulted when the Linode API reports
	// the old instance as missing (404) during a rebuild: the replacement is
	// then created directly from the cached region/type/image instead of
	// failing the task.
	ResolveInstance func(id int) (linode.Instance, bool)
}

func NewManager(api LinodeAPI) *Manager {
	m := &Manager{
		tasks:                map[string]*Task{},
		busy:                 map[int]string{},
		api:                  api,
		RunFn:                sshrun.Run,
		PollInterval:         3 * time.Second,
		PollTimeout:          10 * time.Minute,
		BootScriptAttempts:   3,
		BootScriptRetryDelay: time.Minute,
	}
	m.newAPI = func(pat string) LinodeAPI {
		c := linode.New(pat)
		if m.APIBase != "" {
			c.BaseURL = m.APIBase
		}
		return c
	}
	m.ProbeFn = m.probeHost
	return m
}

// SetSSHPort sets the SSH port used for the boot-script run and probe.
func (m *Manager) SetSSHPort(port int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sshPort = port
}

// Start queues a rebuild job and returns its task id. Rebuilds of the same
// instance are serialized: while one is in flight, Start returns ErrBusy.
// Different instances may be rebuilt concurrently. The pat is the
// Personal Access Token for the requesting user's chosen token; rootPass is the
// instance's saved root password (empty = SSH-key-only), and tokenLabel is
// recorded on the task for display.
func (m *Manager) Start(instanceID int, script, rootPass, pat string, tokenLabel string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.busy[instanceID]; ok {
		return "", ErrBusy
	}
	m.nextID++
	id := fmt.Sprintf("task-%d", m.nextID)
	m.busy[instanceID] = id
	t := &Task{
		ID:         id,
		InstanceID: instanceID,
		Status:     StatusRunning,
		Script:     script,
		TokenLabel: tokenLabel,
		StartedAt:  time.Now().UTC().Format(time.RFC3339),
		Log:        []string{},
		rootPass:   rootPass,
	}
	m.tasks[id] = t
	m.order = append(m.order, id)
	go m.run(t, pat)
	return id, nil
}

// Get returns a snapshot of one task (log newest first).
func (m *Manager) Get(id string) (Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return Task{}, ErrNotFound
	}
	return t.snapshot(), nil
}

// List returns all tasks, newest first, each with its log reversed.
func (m *Manager) List() []Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Task, 0, len(m.order))
	for i := len(m.order) - 1; i >= 0; i-- {
		out = append(out, m.tasks[m.order[i]].snapshot())
	}
	return out
}

func (m *Manager) log(t *Task, format string, args ...any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t.Log = append(t.Log, fmt.Sprintf(format, args...))
}

func (m *Manager) setOldIP(t *Task, ip string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t.OldIP = ip
}

func (m *Manager) succeed(t *Task, newIP string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t.Status = StatusSuccess
	t.NewIP = newIP
	t.EndedAt = time.Now().UTC().Format(time.RFC3339)
	delete(m.busy, t.InstanceID)
}

func (m *Manager) fail(t *Task, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t.Status = StatusFailed
	t.Err = err.Error()
	t.EndedAt = time.Now().UTC().Format(time.RFC3339)
	delete(m.busy, t.InstanceID)
}

// Busy reports whether a rebuild task is currently running for instanceID.
func (m *Manager) Busy(instanceID int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.busy[instanceID]
	return ok
}

// setNewInstanceID records the instance created by a rebuild, so callers
// (e.g. the auto-rebuild monitor) can follow the instance to its new id.
func (m *Manager) setNewInstanceID(t *Task, id int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t.NewInstanceID = id
}
