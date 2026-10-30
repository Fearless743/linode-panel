package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"linode-panel/internal/linode"
)

// linodeAPI is the subset of the Linode client used by the executor. It is an
// interface so tests can substitute a stateful fake.
type linodeAPI interface {
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

// runFn runs the boot script over SSH. Defaults to sshrun.Run; tests inject a
// stub (or a real listener).
type runFn func(ctx context.Context, host string, port int, privPEM []byte, script string) error

// probeFn verifies the instance's SSH port is reachable.
type probeFn func(ctx context.Context, host string, port int) error

// executor carries the agent's credentials and injectable seams.
type executor struct {
	// pats holds the locally-saved Linode PATs (plaintext, in memory).
	pats     []string
	apiBase  string
	sshPort  int
	run      runFn
	probe    probeFn
	progress func(step string) // optional progress callback
	newAPI   func(pat string) linodeAPI
	log      *slog.Logger
}

func (e *executor) api(pat string) linodeAPI {
	if e.newAPI != nil {
		return e.newAPI(pat)
	}
	c := linode.New(pat)
	c.BaseURL = e.apiBase
	return c
}

// isNotFoundOrForbidden reports whether a Linode API error is 404 or 403,
// meaning "this PAT cannot manage the instance" (try the next PAT).
func isNotFoundOrForbidden(err error) bool {
	var apiErr *linode.APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == 404 || apiErr.StatusCode == 403
	}
	return false
}

// listInstances aggregates all instances across every local PAT. A PAT that
// fails (bad token, network) is skipped with a warning. No PATs → empty list.
func (e *executor) listInstances(ctx context.Context) ([]linode.Instance, error) {
	if len(e.pats) == 0 {
		return []linode.Instance{}, nil
	}
	seen := map[int]bool{}
	var all []linode.Instance
	for _, pat := range e.pats {
		api := e.api(pat)
		insts, err := api.ListInstances(ctx)
		if err != nil {
			e.log.Warn("list: 跳过失败的 PAT", "err", err)
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

// powerAction performs one power action, trying each stored PAT until one
// succeeds. A PAT that returns 404/403 is skipped; any other error is a real
// failure.
func (e *executor) powerAction(ctx context.Context, action string, id int) error {
	return e.powerActionWith(ctx, e.pats, action, id)
}

// powerActionWith is powerAction over an explicit PAT list.
func (e *executor) powerActionWith(ctx context.Context, pats []string, action string, id int) error {
	if len(pats) == 0 {
		return errors.New("没有可用的 PAT")
	}
	var lastErr error
	for _, pat := range pats {
		api := e.api(pat)
		var err error
		switch action {
		case "boot":
			err = api.Boot(ctx, id)
		case "shutdown":
			err = api.Shutdown(ctx, id)
		case "reboot":
			err = api.Reboot(ctx, id)
		default:
			return fmt.Errorf("未知电源操作: %s", action)
		}
		if err == nil {
			return nil
		}
		if isNotFoundOrForbidden(err) {
			lastErr = err
			continue
		}
		return fmt.Errorf("执行 %s 失败: %w", action, err)
	}
	return fmt.Errorf("所有 PAT 都无法执行 %s: %w", action, lastErr)
}

// rebuildPollTimeout / rebuildPollInterval bound the status polls.
const (
	rebuildPollTimeout  = 10 * time.Minute
	rebuildPollInterval = 3 * time.Second
	sshExecTimeout      = 5 * time.Minute
)
