package api

import (
	"context"
	"errors"
	"testing"

	"linode-panel/internal/linode"
)

type stubListAPI struct {
	insts []linode.Instance
	err   error
}

func (s *stubListAPI) ListInstances(ctx context.Context) ([]linode.Instance, error) {
	if s.err != nil {
		return nil, s.err
	}
	return append([]linode.Instance(nil), s.insts...), nil
}
func (s *stubListAPI) GetInstance(ctx context.Context, id int) (linode.Instance, error) {
	return linode.Instance{}, nil
}
func (s *stubListAPI) Boot(ctx context.Context, id int) error     { return nil }
func (s *stubListAPI) Shutdown(ctx context.Context, id int) error { return nil }
func (s *stubListAPI) Reboot(ctx context.Context, id int) error   { return nil }
func (s *stubListAPI) GetInstanceIPs(ctx context.Context, id int) (linode.InstanceIPs, error) {
	return linode.InstanceIPs{}, nil
}
func (s *stubListAPI) CreateInstance(ctx context.Context, opts linode.CreateOptions) (linode.Instance, error) {
	return linode.Instance{}, nil
}
func (s *stubListAPI) DeleteInstance(ctx context.Context, id int) error { return nil }
func (s *stubListAPI) AttachAllowAllFirewall(ctx context.Context, region string, linodeID int) error {
	return nil
}

func TestInstanceCacheMonitorRefreshesAllTokens(t *testing.T) {
	srv := testServer(t, &fakeAPI{})
	addAliceToken(t, srv, "a", "pat-a")
	addAliceToken(t, srv, "b", "pat-b")

	stub := &stubListAPI{insts: []linode.Instance{{ID: 7, Label: "web", Status: "running"}}}
	m := &InstanceCacheMonitor{srv: srv, clientFor: func(pat string) LinodeAPI { return stub }}
	m.tickOnce(context.Background())

	for _, tok := range srv.Store.TokensOf(aliceID) {
		rows := srv.Store.CachedInstanceRows(tok.ID)
		if len(rows) != 1 || rows[0].Instance.ID != 7 || rows[0].Instance.Status != "running" {
			t.Fatalf("token %d cache = %+v, want instance 7 running", tok.ID, rows)
		}
	}
}

func TestInstanceCacheMonitorKeepsOldCacheOnFailure(t *testing.T) {
	srv := testServer(t, &fakeAPI{})
	addAliceToken(t, srv, "a", "pat-a")
	tok := srv.Store.TokensOf(aliceID)[0]
	if err := srv.Store.CacheInstances(tok.ID, []linode.Instance{{ID: 1, Label: "old", Status: "offline"}}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	stub := &stubListAPI{err: errors.New("boom")}
	m := &InstanceCacheMonitor{srv: srv, clientFor: func(pat string) LinodeAPI { return stub }}
	m.tickOnce(context.Background())

	rows := srv.Store.CachedInstanceRows(tok.ID)
	if len(rows) != 1 || rows[0].Instance.ID != 1 || rows[0].Instance.Status != "offline" {
		t.Fatalf("cache after failure = %+v, want kept instance 1 offline", rows)
	}
}
