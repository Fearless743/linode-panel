package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"linode-panel/internal/linode"
)

// cacheLinode is a controllable fake of the Linode API for instance listing
// and power actions. It counts requests and can be told to fail or to change
// its instance set, so tests can exercise the instance cache paths.
type cacheLinode struct {
	mu    sync.Mutex
	insts []linode.Instance
	fail  bool
	reqs  int
}

func (c *cacheLinode) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reqs
}

func (c *cacheLinode) setFail(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fail = v
}

func (c *cacheLinode) setInsts(insts []linode.Instance) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.insts = append([]linode.Instance(nil), insts...)
}

func (c *cacheLinode) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.reqs++
		fail := c.fail
		insts := append([]linode.Instance(nil), c.insts...)
		c.mu.Unlock()

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/linode/instances":
			if fail {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte(`{"errors":[{"reason":"boom"}]}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": insts, "page": 1, "pages": 1})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/linode/instances/"):
			if fail {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte(`{"errors":[{"reason":"boom"}]}`))
				return
			}
			id := strings.TrimPrefix(r.URL.Path, "/linode/instances/")
			for i := range insts {
				if strconv.Itoa(insts[i].ID) == id {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(insts[i])
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[{"reason":"not found"}]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/boot"):
			c.mu.Lock()
			for i := range c.insts {
				c.insts[i].Status = "running"
			}
			c.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{}"))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[{"reason":"not found"}]}`))
		}
	})
}

// cacheServerFor wires a Server whose APIBase points at a controllable
// cacheLinode, with alice logged in and one PAT ("main").
func cacheServerFor(t *testing.T, cl *cacheLinode) (*Server, *http.Cookie) {
	t.Helper()
	ts := httptest.NewServer(cl.handler())
	t.Cleanup(ts.Close)
	srv := testServer(t, &fakeAPI{})
	srv.APIBase = ts.URL
	addAliceToken(t, srv, "main", "pat1")
	return srv, loginAlice(t, srv)
}

// listResult is the decoded /api/instances response used by the cache tests.
type listResult struct {
	Instances []struct {
		ID         int    `json:"id"`
		Label      string `json:"label"`
		Status     string `json:"status"`
		FetchError string `json:"fetch_error"`
		Missing    bool   `json:"missing"`
	} `json:"instances"`
}

// getList fetches /api/instances, optionally forcing a refresh (?refresh=1).
func getList(t *testing.T, srv *Server, cookie *http.Cookie, refresh bool) listResult {
	t.Helper()
	path := "/api/instances"
	if refresh {
		path += "?refresh=1"
	}
	rr := authedGet(t, srv, cookie, path)
	wantStatus(t, rr, http.StatusOK)
	var res listResult
	decodeJSON(t, rr, &res)
	return res
}

func TestInstanceListServesCacheUnlessRefresh(t *testing.T) {
	cl := &cacheLinode{insts: []linode.Instance{
		{ID: 1, Label: "web", Status: "running", IPv4: []string{"1.2.3.4"}},
	}}
	srv, cookie := cacheServerFor(t, cl)

	// First list (no cache yet) fetches from the API and stores the snapshot.
	res := getList(t, srv, cookie, false)
	if cl.count() != 1 {
		t.Fatalf("api hits after first list = %d, want 1", cl.count())
	}
	if len(res.Instances) != 1 || res.Instances[0].ID != 1 {
		t.Fatalf("first list = %+v, want instance 1", res.Instances)
	}

	// A non-refresh list serves the cache and never hits the API, even when
	// the API is down.
	cl.setFail(true)
	before := cl.count()
	res = getList(t, srv, cookie, false)
	if cl.count() != before {
		t.Fatalf("cached list should not hit the API: %d -> %d", before, cl.count())
	}
	if len(res.Instances) != 1 || res.Instances[0].ID != 1 ||
		res.Instances[0].Status != "running" || res.Instances[0].FetchError != "" {
		t.Fatalf("cached list = %+v, want id 1 running with no fetch_error", res.Instances)
	}

	// A manual refresh hits the API; on failure it falls back to the cache
	// and flags the stale data.
	before = cl.count()
	res = getList(t, srv, cookie, true)
	if cl.count() != before+1 {
		t.Fatalf("refresh should hit the API: %d -> %d", before, cl.count())
	}
	if len(res.Instances) != 1 || res.Instances[0].ID != 1 ||
		res.Instances[0].Status != "running" {
		t.Fatalf("refresh fallback list = %+v, want cached id 1 running", res.Instances)
	}
	if res.Instances[0].FetchError == "" {
		t.Fatal("refresh failure must carry a fetch_error")
	}
}

func TestInstanceListRefreshForcesAPI(t *testing.T) {
	cl := &cacheLinode{insts: []linode.Instance{
		{ID: 1, Label: "web", Status: "offline", IPv4: []string{"1.2.3.4"}},
	}}
	srv, cookie := cacheServerFor(t, cl)

	// Populate the cache with the stale "offline" state.
	res := getList(t, srv, cookie, false)
	if len(res.Instances) != 1 || res.Instances[0].Status != "offline" {
		t.Fatalf("initial list = %+v, want id 1 offline", res.Instances)
	}

	// The instance is booted elsewhere; only a manual refresh sees it.
	cl.setInsts([]linode.Instance{{ID: 1, Label: "web", Status: "running", IPv4: []string{"1.2.3.4"}}})
	before := cl.count()
	res = getList(t, srv, cookie, true)
	if cl.count() != before+1 {
		t.Fatalf("refresh should hit the API: %d -> %d", before, cl.count())
	}
	if len(res.Instances) != 1 || res.Instances[0].Status != "running" {
		t.Fatalf("refresh list = %+v, want id 1 running", res.Instances)
	}
	if res.Instances[0].FetchError != "" {
		t.Fatalf("fresh refresh must not carry fetch_error: %+v", res.Instances[0])
	}

	// The refreshed snapshot is now cached; a non-refresh list serves it
	// without hitting the API.
	before = cl.count()
	res = getList(t, srv, cookie, false)
	if cl.count() != before {
		t.Fatalf("cached list after refresh should not hit the API: %d -> %d", before, cl.count())
	}
	if len(res.Instances) != 1 || res.Instances[0].Status != "running" {
		t.Fatalf("cached list = %+v, want id 1 running", res.Instances)
	}
}

func TestPowerActionInvalidatesCache(t *testing.T) {
	cl := &cacheLinode{insts: []linode.Instance{
		{ID: 1, Label: "web", Status: "offline", IPv4: []string{"1.2.3.4"}},
	}}
	srv, cookie := cacheServerFor(t, cl)

	// Populate the cache (1 API hit), then boot (2nd hit, invalidates cache).
	getList(t, srv, cookie, false)
	rr := authedPost(t, srv, cookie, "/api/instances/1/boot", "")
	wantStatus(t, rr, http.StatusOK)
	before := cl.count()

	// The next list has no cache (invalidated), so it refetches and sees the
	// new "running" state instead of a stale snapshot.
	res := getList(t, srv, cookie, false)
	if cl.count() != before+1 {
		t.Fatalf("list after power action should refetch the API: %d -> %d", before, cl.count())
	}
	if len(res.Instances) != 1 || res.Instances[0].Status != "running" {
		t.Fatalf("list after boot = %+v, want id 1 running", res.Instances)
	}
}

func TestInstanceRefreshUpdatesRowAndCache(t *testing.T) {
	cl := &cacheLinode{insts: []linode.Instance{
		{ID: 1, Label: "web", Status: "offline", Region: "us-east", Type: "g6-standard-1",
			Image: "linode/debian12", IPv4: []string{"1.2.3.4"}, Tags: []string{"prod"}},
	}}
	srv, cookie := cacheServerFor(t, cl)

	// Populate the cache with the stale "offline" state.
	if res := getList(t, srv, cookie, false); len(res.Instances) != 1 || res.Instances[0].Status != "offline" {
		t.Fatalf("initial list = %+v, want id 1 offline", res.Instances)
	}

	// The instance is booted elsewhere; the per-row refresh sees it.
	cl.setInsts([]linode.Instance{
		{ID: 1, Label: "web", Status: "running", Region: "us-east", Type: "g6-standard-1",
			Image: "linode/debian12", IPv4: []string{"5.6.7.8"}, Tags: []string{"prod"}},
	})
	before := cl.count()
	rr := authedGet(t, srv, cookie, "/api/instances/1/refresh")
	wantStatus(t, rr, http.StatusOK)
	if cl.count() != before+1 {
		t.Fatalf("row refresh should hit the API: %d -> %d", before, cl.count())
	}
	var row struct {
		ID         int      `json:"id"`
		Label      string   `json:"label"`
		Status     string   `json:"status"`
		Region     string   `json:"region"`
		Type       string   `json:"type"`
		Image      string   `json:"image"`
		IPv4       []string `json:"ipv4"`
		Tags       []string `json:"tags"`
		TokenID    int      `json:"token_id"`
		TokenLabel string   `json:"token_label"`
	}
	decodeJSON(t, rr, &row)
	if row.ID != 1 || row.Status != "running" || row.Region != "us-east" ||
		row.Type != "g6-standard-1" || row.Image != "linode/debian12" ||
		len(row.IPv4) != 1 || row.IPv4[0] != "5.6.7.8" ||
		len(row.Tags) != 1 || row.Tags[0] != "prod" ||
		row.TokenLabel != "main" || row.TokenID == 0 {
		t.Fatalf("refresh row = %+v", row)
	}

	// The refreshed snapshot is now cached: a non-refresh list serves it
	// without hitting the API.
	before = cl.count()
	res := getList(t, srv, cookie, false)
	if cl.count() != before {
		t.Fatalf("cached list after row refresh should not hit the API: %d -> %d", before, cl.count())
	}
	if len(res.Instances) != 1 || res.Instances[0].Status != "running" {
		t.Fatalf("cached list = %+v, want id 1 running", res.Instances)
	}
}

// TestInstanceRefreshMarksMissingInCache covers the "unavailable instance"
// behavior: when the row-level refresh learns the instance no longer exists
// upstream (404), the cached row is kept (marked unavailable) instead of being
// dropped, so the user can still see it and migrate it to another account.
func TestInstanceRefreshMarksMissingInCache(t *testing.T) {
	cl := &cacheLinode{insts: []linode.Instance{
		{ID: 1, Label: "web", Status: "running", IPv4: []string{"1.2.3.4"}},
	}}
	srv, cookie := cacheServerFor(t, cl)

	// Cache the instance.
	if res := getList(t, srv, cookie, false); len(res.Instances) != 1 || res.Instances[0].ID != 1 {
		t.Fatalf("initial list = %+v, want instance 1", res.Instances)
	}

	// The instance is deleted upstream; the per-row refresh now 404s.
	cl.setInsts(nil)
	rr := authedGet(t, srv, cookie, "/api/instances/1/refresh")
	wantStatus(t, rr, http.StatusNotFound)

	// The row is kept and reported as unavailable, not evicted.
	res := getList(t, srv, cookie, false)
	if len(res.Instances) != 1 || !res.Instances[0].Missing || res.Instances[0].Status != "unavailable" {
		t.Fatalf("list after 404 refresh = %+v, want kept unavailable instance", res.Instances)
	}
}

// TestInstanceStatusMarksMissingInCache verifies the status endpoint marks a
// stale cached row unavailable on 404 too, so polling a deleted instance
// cannot leave it looking healthy.
func TestInstanceStatusMarksMissingInCache(t *testing.T) {
	cl := &cacheLinode{insts: []linode.Instance{
		{ID: 1, Label: "web", Status: "running", IPv4: []string{"1.2.3.4"}},
	}}
	srv, cookie := cacheServerFor(t, cl)

	if res := getList(t, srv, cookie, false); len(res.Instances) != 1 || res.Instances[0].ID != 1 {
		t.Fatalf("initial list = %+v, want instance 1", res.Instances)
	}

	cl.setInsts(nil)
	rr := authedGet(t, srv, cookie, "/api/instances/1/status")
	wantStatus(t, rr, http.StatusNotFound)

	res := getList(t, srv, cookie, false)
	if len(res.Instances) != 1 || !res.Instances[0].Missing || res.Instances[0].Status != "unavailable" {
		t.Fatalf("list after 404 status = %+v, want kept unavailable instance", res.Instances)
	}
}
