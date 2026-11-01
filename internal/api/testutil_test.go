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
	"linode-panel/internal/store"
)

// testServer wires a real store (t.TempDir) and a Server backed by api.
// The store is seeded with a deterministic test user "alice"/"alice123" in
// the built-in admin group (id 1, all permissions).
func testServer(t *testing.T, api LinodeAPI) *Server {
	t.Helper()
	st, _, err := store.Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if _, err := st.CreateUser("alice", "alice123", 1); err != nil {
		t.Fatalf("CreateUser alice: %v", err)
	}
	srv := New(api, st)
	t.Cleanup(func() { _ = st.Save() })
	return srv
}

// openTestStore opens a fresh seeded store in a temp dir.
func openTestStore(t *testing.T) (*store.Store, error) {
	st, _, err := store.Open(t.TempDir() + "/state.db")
	return st, err
}

// testServerWithStore wraps an existing store into a Server backed by api.
func testServerWithStore(t *testing.T, st *store.Store) *Server {
	t.Helper()
	srv := New(&fakeAPI{}, st)
	t.Cleanup(func() { _ = st.Save() })
	return srv
}

// loginAs logs username/password in and returns the session cookie.
func loginAs(t *testing.T, srv *Server, username, password string) *http.Cookie {
	t.Helper()
	body := strings.NewReader(`{"username":"` + username + `","password":"` + password + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", body)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("login %s status = %d, want 200 (body=%s)", username, rr.Code, rr.Body.String())
	}
	for _, c := range rr.Result().Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatalf("login %s did not set session cookie", username)
	return nil
}

// loginAlice logs alice in and returns a session cookie for authenticated
// requests.
func loginAlice(t *testing.T, srv *Server) *http.Cookie {
	t.Helper()
	body := strings.NewReader(`{"username":"alice","password":"alice123"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", body)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	for _, c := range rr.Result().Cookies() {
		if c.Name == "panel_session" {
			return c
		}
	}
	t.Fatal("login did not set panel_session cookie")
	return nil
}

// aliceID is the deterministic user id of the "alice" test user created by
// testServer (the seeded admin is id 1, so alice is id 2).
const aliceID = 2

// addAliceToken adds a PAT to alice and fails the test on error.
func addAliceToken(t *testing.T, srv *Server, label, pat string) {
	t.Helper()
	if _, err := srv.Store.AddToken(aliceID, label, pat); err != nil {
		t.Fatalf("AddToken(%s): %v", label, err)
	}
}

// authedGet performs a GET with the given session cookie.
func authedGet(t *testing.T, srv *Server, cookie *http.Cookie, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	return rr
}

// authedPost performs a POST with the given session cookie and optional body.
func authedPost(t *testing.T, srv *Server, cookie *http.Cookie, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *strings.Reader
	if body == "" {
		rdr = strings.NewReader("")
	} else {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(http.MethodPost, path, rdr)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	return rr
}

// authedPatch performs a PATCH with the given session cookie and body.
func authedPatch(t *testing.T, srv *Server, cookie *http.Cookie, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return doReq(t, srv, cookie, http.MethodPatch, path, body)
}

// authedPut performs a PUT with the given session cookie and body.
func authedPut(t *testing.T, srv *Server, cookie *http.Cookie, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return doReq(t, srv, cookie, http.MethodPut, path, body)
}

// authedDelete performs a DELETE with the given session cookie.
func authedDelete(t *testing.T, srv *Server, cookie *http.Cookie, path string) *httptest.ResponseRecorder {
	t.Helper()
	return doReq(t, srv, cookie, http.MethodDelete, path, "")
}

// doReq performs one HTTP request against srv with cookie and optional body.
func doReq(t *testing.T, srv *Server, cookie *http.Cookie, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	return rr
}

// fakeLinodeServer returns an httptest server that mimics the Linode API v4
// for instance listing and power actions, keyed by PAT via the Authorization
// header. Each PAT maps to a fixed instance set.
func fakeLinodeServer(t *testing.T, byPAT map[string][]linode.Instance) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	statuses := map[int]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		pat := strings.TrimPrefix(auth, "Bearer ")
		mu.Lock()
		insts := byPAT[pat]
		mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/linode/instances":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": insts, "page": 1, "pages": 1,
			})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/linode/instances/") && strings.HasSuffix(r.URL.Path, "/ips"):
			// GET /linode/instances/{id}/ips -> InstanceIPs.
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/linode/instances/"), "/")
			id := parts[0]
			var found *linode.Instance
			for i := range insts {
				if id == strconv.Itoa(insts[i].ID) {
					found = &insts[i]
					break
				}
			}
			if found == nil {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"reason": "not found"}}})
				return
			}
			ips := linode.InstanceIPs{}
			for _, a := range found.IPv4 {
				ips.IPv4.Public = append(ips.IPv4.Public, linode.IPAddress{Address: a, Public: true})
			}
			if found.IPv6 != "" {
				ips.IPv6.SLAAC = linode.IPAddress{Address: found.IPv6}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(ips)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/linode/instances/"):
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/linode/instances/"), "/")
			id := parts[0]
			var found *linode.Instance
			for i := range insts {
				if id == strconv.Itoa(insts[i].ID) {
					found = &insts[i]
					break
				}
			}
			if found == nil {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"reason": "not found"}}})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(found)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/linode/instances/"):
			// DELETE /linode/instances/{id} -> 200 when the instance exists.
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/linode/instances/"), "/")
			id := parts[0]
			var found *linode.Instance
			for i := range insts {
				if id == strconv.Itoa(insts[i].ID) {
					found = &insts[i]
					break
				}
			}
			if found == nil {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"reason": "not found"}}})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{}"))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/boot"):
			mu.Lock()
			statuses[1] = "running"
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{}"))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/shutdown"):
			mu.Lock()
			statuses[1] = "offline"
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{}"))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/reboot"):
			mu.Lock()
			statuses[1] = "running"
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{}"))
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"reason": "not found"}}})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// decodeJSONInto decodes rr body into v.
func decodeJSONInto(t *testing.T, rr *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rr.Body.Bytes(), v); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rr.Body.String())
	}
}
