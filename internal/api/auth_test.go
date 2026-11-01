package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUnauthenticated401(t *testing.T) {
	srv := testServer(t, &fakeAPI{})
	for _, path := range []string{
		"/api/instances",
		"/api/instances/1/status",
		"/api/users",
		"/api/groups",
	} {
		rr := authedGet(t, srv, &http.Cookie{Name: "wrong", Value: "x"}, path)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d, want 401 (body=%s)", path, rr.Code, rr.Body.String())
		}
		var resp struct {
			Error string `json:"error"`
		}
		decodeJSON(t, rr, &resp)
		if resp.Error != "未登录或会话已过期" {
			t.Fatalf("%s: error = %q", path, resp.Error)
		}
	}
	// POST-only power route.
	rr := authedPost(t, srv, &http.Cookie{Name: "wrong", Value: "x"}, "/api/instances/1/boot", "")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("boot: status = %d, want 401 (body=%s)", rr.Code, rr.Body.String())
	}
}

func TestExpiredSession401(t *testing.T) {
	srv := testServer(t, &fakeAPI{})
	// A session value that never existed resolves to no user.
	req := httptest.NewRequest(http.MethodGet, "/api/instances", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "no-such-session"})
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body=%s)", rr.Code, rr.Body.String())
	}
}

func TestPermissionForbidden403(t *testing.T) {
	// Create a user in the read-only group (id 2, PermView only) so power
	// routes return 403 while the list route returns 200.
	st, err := openTestStore(t)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser("reader", "reader123", 2); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	srv := testServerWithStore(t, st)
	cookie := loginAs(t, srv, "reader", "reader123")

	rr := authedPost(t, srv, cookie, "/api/instances/1/boot", "")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("boot status = %d, want 403 (body=%s)", rr.Code, rr.Body.String())
	}
	var resp struct {
		Error string `json:"error"`
	}
	decodeJSON(t, rr, &resp)
	if resp.Error != "权限不足" {
		t.Fatalf("error = %q", resp.Error)
	}
	// view-only user can still list (empty PAT list -> 200 empty).
	rr2 := authedGet(t, srv, cookie, "/api/instances")
	if rr2.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200 (body=%s)", rr2.Code, rr2.Body.String())
	}
	// view-only cannot access admin routes.
	rr3 := authedGet(t, srv, cookie, "/api/users")
	if rr3.Code != http.StatusForbidden {
		t.Fatalf("users status = %d, want 403", rr3.Code)
	}
}

func TestLoginLogoutMe(t *testing.T) {
	srv := testServer(t, &fakeAPI{})
	// Wrong password -> uniform 401.
	bad := authedPost(t, srv, nil, "/api/auth/login", `{"username":"alice","password":"nope"}`)
	if bad.Code != http.StatusUnauthorized {
		t.Fatalf("bad login status = %d, want 401 (body=%s)", bad.Code, bad.Body.String())
	}
	var errResp struct {
		Error string `json:"error"`
	}
	decodeJSON(t, bad, &errResp)
	if errResp.Error != "用户名或密码错误" {
		t.Fatalf("bad login error = %q", errResp.Error)
	}

	// Correct login sets a session cookie and returns username+perms.
	cookie := loginAlice(t, srv)
	if cookie == nil || cookie.Name != sessionCookie || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie malformed: %+v", cookie)
	}
	rr := authedGet(t, srv, cookie, "/api/auth/me")
	if rr.Code != http.StatusOK {
		t.Fatalf("me status = %d, want 200", rr.Code)
	}
	var me struct {
		Username string   `json:"username"`
		Perms    []string `json:"perms"`
	}
	decodeJSON(t, rr, &me)
	if me.Username != "alice" {
		t.Fatalf("me username = %q", me.Username)
	}
	if len(me.Perms) != 5 {
		t.Fatalf("me perms = %#v, want 5 admin perms", me.Perms)
	}

	// me without cookie -> 401.
	meNo := authedGet(t, srv, nil, "/api/auth/me")
	if meNo.Code != http.StatusUnauthorized {
		t.Fatalf("me no-cookie status = %d, want 401", meNo.Code)
	}

	// logout drops the session; me then 401.
	lo := authedPost(t, srv, cookie, "/api/auth/logout", "")
	if lo.Code != http.StatusOK {
		t.Fatalf("logout status = %d, want 200", lo.Code)
	}
	meAfter := authedGet(t, srv, cookie, "/api/auth/me")
	if meAfter.Code != http.StatusUnauthorized {
		t.Fatalf("me after logout status = %d, want 401", meAfter.Code)
	}
}

func TestLoginBadBody(t *testing.T) {
	srv := testServer(t, &fakeAPI{})
	rr := authedPost(t, srv, nil, "/api/auth/login", `{"username":""}`)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}
