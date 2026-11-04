package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"linode-panel/internal/sshrun"
	"linode-panel/internal/store"
)

// newCreateScriptServer wires a store with the seeded admin (id 1), one PAT and
// a session, mirroring rebuildServer but without the rebuild harness.
func newCreateScriptServer(t *testing.T) (*Server, string) {
	t.Helper()
	st, _, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if _, err := st.AddToken(1, "test-token", "linode_pat_test"); err != nil {
		t.Fatalf("AddToken: %v", err)
	}
	session := st.NewSession(1, time.Hour)
	s := New(&fakeAPI{}, st)
	t.Cleanup(func() { _ = st.Save() })
	return s, session
}

// createScriptMock serves the minimal Linode API needed by the create handler
// and the background boot-script runner.
func createScriptMock(t *testing.T, createBody *map[string]any, mu *sync.Mutex) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/linode/instances":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			*createBody = body
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":200,"label":"boot","status":"provisioning","region":"us-east","type":"g6-standard-1","image":"linode/debian12","ipv4":[],"ipv6":""}`)
		case r.Method == http.MethodGet && r.URL.Path == "/linode/instances/200":
			_, _ = io.WriteString(w, `{"id":200,"label":"boot","status":"running","region":"us-east","type":"g6-standard-1","image":"linode/debian12","ipv4":["198.51.100.9"],"ipv6":""}`)
		case r.Method == http.MethodGet && r.URL.Path == "/linode/instances/200/ips":
			_, _ = io.WriteString(w, `{"ipv4":{"public":[{"address":"198.51.100.9","public":true}]},"ipv6":{}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/networking/firewalls":
			_, _ = io.WriteString(w, `{"data":[],"page":1,"pages":1}`)
		case r.Method == http.MethodPost && r.URL.Path == "/networking/firewalls":
			_, _ = io.WriteString(w, `{"id":5,"label":"linode-panel-allow-all","region":"us-east","status":"enabled","tags":["linode-panel-allow-all"]}`)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/networking/firewalls/") && strings.HasSuffix(r.URL.Path, "/devices"):
			_, _ = io.WriteString(w, `{"data":[],"page":1,"pages":1}`)
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/networking/firewalls/") && strings.HasSuffix(r.URL.Path, "/devices"):
			_, _ = io.WriteString(w, `{"id":1,"entity":{"id":200,"type":"linode","label":"boot"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"errors":[{"reason":"not found"}]}`)
		}
	}))
}

// TestCreateInstanceRunsBootScript verifies that a create request with a
// non-empty script returns before the instance has booted and runs the script
// over SSH with the root password once the instance is running, exposing the
// result via GET /api/instances/{id}/script.
func TestCreateInstanceRunsBootScript(t *testing.T) {
	var (
		mu         sync.Mutex
		createBody map[string]any
	)
	mock := createScriptMock(t, &createBody, &mu)
	defer mock.Close()

	srv, session := newCreateScriptServer(t)
	srv.APIBase = mock.URL
	srv.BootScriptPollInterval = time.Millisecond

	type sshCall struct {
		opts   sshrun.Options
		script string
	}
	calls := make(chan sshCall, 1)
	srv.BootScriptRun = func(_ context.Context, opts sshrun.Options, script string) (sshrun.Result, error) {
		calls <- sshCall{opts: opts, script: script}
		return sshrun.Result{Exit: 0}, nil
	}

	// The handler trims surrounding whitespace before saving/running.
	const script = "#!/usr/bin/env bash\necho hi"
	body := `{"token_id":1,"label":"boot","region":"us-east","type":"g6-standard-1","image":"linode/debian12","root_pass":"Secret123!","script":"#!/usr/bin/env bash\necho hi\n"}`
	req := httptest.NewRequest(http.MethodPost, "/api/account/instances", strings.NewReader(body))
	authed(req, session)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	wantStatus(t, rr, http.StatusCreated)

	_ = createBody

	select {
	case call := <-calls:
		if call.opts.Host != "198.51.100.9" {
			t.Fatalf("ssh host = %q, want 198.51.100.9", call.opts.Host)
		}
		if call.script != script {
			t.Fatalf("ssh script = %q, want %q", call.script, script)
		}
		if call.opts.Password != "Secret123!" {
			t.Fatal("ssh password not passed through")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("boot script was not executed over SSH")
	}

	// The run state is exposed alongside the saved script.
	deadline := time.Now().Add(2 * time.Second)
	for {
		st, ok := srv.bootScriptRun(200)
		if ok && st.State == ScriptRunSuccess {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("script run state = %+v, want success", st)
		}
		time.Sleep(time.Millisecond)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/instances/200/script", nil)
	authed(getReq, session)
	getRR := httptest.NewRecorder()
	srv.Handler().ServeHTTP(getRR, getReq)
	wantStatus(t, getRR, http.StatusOK)
	var got struct {
		Script     string         `json:"script"`
		Configured bool           `json:"configured"`
		LastRun    scriptRunState `json:"last_run"`
	}
	decodeJSON(t, getRR, &got)
	if got.Script != script || !got.Configured {
		t.Fatalf("script response = %+v, want configured script", got)
	}
	if got.LastRun.State != ScriptRunSuccess || got.LastRun.Host != "198.51.100.9" {
		t.Fatalf("last_run = %+v, want success on 198.51.100.9", got.LastRun)
	}
}

// TestCreateInstanceWithoutScriptSkipsBootScript verifies that an empty script
// does not start the runner.
func TestCreateInstanceWithoutScriptSkipsBootScript(t *testing.T) {
	var (
		mu         sync.Mutex
		createBody map[string]any
	)
	mock := createScriptMock(t, &createBody, &mu)
	defer mock.Close()

	srv, session := newCreateScriptServer(t)
	srv.APIBase = mock.URL
	srv.BootScriptPollInterval = time.Millisecond

	ran := make(chan struct{}, 1)
	srv.BootScriptRun = func(context.Context, sshrun.Options, string) (sshrun.Result, error) {
		ran <- struct{}{}
		return sshrun.Result{Exit: 0}, nil
	}

	body := `{"token_id":1,"label":"boot","region":"us-east","type":"g6-standard-1","image":"linode/debian12","root_pass":"Secret123!"}`
	req := httptest.NewRequest(http.MethodPost, "/api/account/instances", strings.NewReader(body))
	authed(req, session)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	wantStatus(t, rr, http.StatusCreated)

	select {
	case <-ran:
		t.Fatal("boot script ran for a request without a script")
	case <-time.After(100 * time.Millisecond):
	}
}
