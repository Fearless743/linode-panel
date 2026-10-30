package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"linode-panel/internal/store"
)

// adminServer returns a server with alice (admin, id 1) logged in.
func adminServer(t *testing.T) (*Server, *http.Cookie) {
	t.Helper()
	srv := testServer(t, &fakeAPI{})
	return srv, loginAlice(t, srv)
}

func TestUsersCRUD(t *testing.T) {
	srv, cookie := adminServer(t)
	// Create.
	rr := authedPost(t, srv, cookie, "/api/users", `{"username":"bob","password":"bobpass1","group_id":2}`)
	wantStatus(t, rr, http.StatusOK)
	// List shows bob with group name and no tokens.
	rr = authedGet(t, srv, cookie, "/api/users")
	wantStatus(t, rr, http.StatusOK)
	var users struct {
		Users []struct {
			ID         int    `json:"id"`
			Username   string `json:"username"`
			GroupID    int    `json:"group_id"`
			GroupName  string `json:"group_name"`
			TokenCount int    `json:"token_count"`
		} `json:"users"`
	}
	decodeJSON(t, rr, &users)
	if len(users.Users) != 3 {
		t.Fatalf("users = %d, want 3 (admin, alice, bob)", len(users.Users))
	}
	var bobID int
	for _, u := range users.Users {
		if u.Username == "bob" {
			bobID = u.ID
			if u.GroupID != 2 || u.GroupName != "只读" || u.TokenCount != 0 {
				t.Fatalf("bob = %+v", u)
			}
		}
	}
	if bobID == 0 {
		t.Fatal("bob not found in list")
	}
	// PATCH group + password.
	rr = authedPatch(t, srv, cookie, "/api/users/"+itoa(bobID), `{"group_id":3,"password":"newpass1"}`)
	wantStatus(t, rr, http.StatusOK)
	rr = authedGet(t, srv, cookie, "/api/users")
	decodeJSON(t, rr, &users)
	for _, u := range users.Users {
		if u.ID == bobID && u.GroupID != 3 {
			t.Fatalf("bob group after patch = %d, want 3", u.GroupID)
		}
	}
	// Password actually changed: bob can log in with new password.
	cookieBob := loginAs(t, srv, "bob", "newpass1")
	if cookieBob == nil {
		t.Fatal("bob login with new password failed")
	}
	// DELETE.
	rr = authedDelete(t, srv, cookie, "/api/users/"+itoa(bobID))
	wantStatus(t, rr, http.StatusOK)
	rr = authedGet(t, srv, cookie, "/api/users")
	decodeJSON(t, rr, &users)
	if len(users.Users) != 2 {
		t.Fatalf("users after delete = %d, want 2 (admin, alice)", len(users.Users))
	}
}

func TestUsersValidation(t *testing.T) {
	srv, cookie := adminServer(t)
	// Short password -> 400 Chinese.
	rr := authedPost(t, srv, cookie, "/api/users", `{"username":"x","password":"short","group_id":2}`)
	wantStatus(t, rr, http.StatusBadRequest)
	var resp struct {
		Error string `json:"error"`
	}
	decodeJSON(t, rr, &resp)
	if resp.Error != "密码长度至少 6 位" {
		t.Fatalf("error = %q", resp.Error)
	}
	// Empty username -> 400.
	rr = authedPost(t, srv, cookie, "/api/users", `{"username":"","password":"123456","group_id":2}`)
	wantStatus(t, rr, http.StatusBadRequest)
	// Bad group -> 400 with store error.
	rr = authedPost(t, srv, cookie, "/api/users", `{"username":"c","password":"123456","group_id":99}`)
	wantStatus(t, rr, http.StatusBadRequest)
	decodeJSON(t, rr, &resp)
	if !strings.Contains(resp.Error, "权限组") {
		t.Fatalf("error = %q", resp.Error)
	}
}

func TestTokenMasked(t *testing.T) {
	srv, cookie := adminServer(t)
	// Add a PAT to alice (id 1).
	rr := authedPost(t, srv, cookie, "/api/users/1/tokens", `{"label":"prod","token":"linode_pat_secret1234"}`)
	wantStatus(t, rr, http.StatusOK)
	// List tokens: masked only, no plaintext leak.
	rr = authedGet(t, srv, cookie, "/api/users/1/tokens")
	wantStatus(t, rr, http.StatusOK)
	var tokens struct {
		Tokens []struct {
			ID     int    `json:"id"`
			Label  string `json:"label"`
			Masked string `json:"masked"`
		} `json:"tokens"`
	}
	decodeJSON(t, rr, &tokens)
	if len(tokens.Tokens) != 1 {
		t.Fatalf("tokens = %d, want 1", len(tokens.Tokens))
	}
	tok := tokens.Tokens[0]
	if tok.Label != "prod" || tok.Masked == "" || tok.Masked == "linode_pat_secret1234" {
		t.Fatalf("token = %+v", tok)
	}
	if tok.Masked != store.Masked("linode_pat_secret1234") {
		t.Fatalf("masked = %q, want %q", tok.Masked, store.Masked("linode_pat_secret1234"))
	}
	// Empty label defaults to "PAT".
	rr = authedPost(t, srv, cookie, "/api/users/1/tokens", `{"label":"","token":"another_secret"}`)
	wantStatus(t, rr, http.StatusOK)
	rr = authedGet(t, srv, cookie, "/api/users/1/tokens")
	decodeJSON(t, rr, &tokens)
	if len(tokens.Tokens) != 2 {
		t.Fatalf("tokens after add = %d, want 2", len(tokens.Tokens))
	}
	if tokens.Tokens[1].Label != "PAT" {
		t.Fatalf("second token label = %q, want PAT", tokens.Tokens[1].Label)
	}
	// Delete a token.
	rr = authedDelete(t, srv, cookie, "/api/users/1/tokens/"+itoa(tokens.Tokens[0].ID))
	wantStatus(t, rr, http.StatusOK)
	rr = authedGet(t, srv, cookie, "/api/users/1/tokens")
	decodeJSON(t, rr, &tokens)
	if len(tokens.Tokens) != 1 {
		t.Fatalf("tokens after delete = %d, want 1", len(tokens.Tokens))
	}
}

func TestGroupsCRUD(t *testing.T) {
	srv, cookie := adminServer(t)
	// Create.
	rr := authedPost(t, srv, cookie, "/api/groups", `{"name":"ops","perms":["view","power"]}`)
	wantStatus(t, rr, http.StatusOK)
	rr = authedGet(t, srv, cookie, "/api/groups")
	wantStatus(t, rr, http.StatusOK)
	var groups struct {
		Groups []struct {
			ID        int      `json:"id"`
			Name      string   `json:"name"`
			Perms     []string `json:"perms"`
			UserCount int      `json:"user_count"`
		} `json:"groups"`
	}
	decodeJSON(t, rr, &groups)
	if len(groups.Groups) != 4 {
		t.Fatalf("groups = %d, want 4", len(groups.Groups))
	}
	var opsID int
	for _, g := range groups.Groups {
		if g.Name == "ops" {
			opsID = g.ID
			if len(g.Perms) != 2 || g.UserCount != 0 {
				t.Fatalf("ops = %+v", g)
			}
		}
	}
	if opsID == 0 {
		t.Fatal("ops group not found")
	}
	// Update (rename + perms).
	rr = authedPut(t, srv, cookie, "/api/groups/"+itoa(opsID), `{"name":"ops2","perms":["view"]}`)
	wantStatus(t, rr, http.StatusOK)
	rr = authedGet(t, srv, cookie, "/api/groups")
	decodeJSON(t, rr, &groups)
	for _, g := range groups.Groups {
		if g.ID == opsID && (g.Name != "ops2" || len(g.Perms) != 1) {
			t.Fatalf("ops after update = %+v", g)
		}
	}
	// Delete.
	rr = authedDelete(t, srv, cookie, "/api/groups/"+itoa(opsID))
	wantStatus(t, rr, http.StatusOK)
	rr = authedGet(t, srv, cookie, "/api/groups")
	decodeJSON(t, rr, &groups)
	if len(groups.Groups) != 3 {
		t.Fatalf("groups after delete = %d, want 3", len(groups.Groups))
	}
}

func TestDeleteReferencedGroup400(t *testing.T) {
	srv, cookie := adminServer(t)
	// Group 1 (admin) is referenced by alice; deleting it must 400.
	rr := authedDelete(t, srv, cookie, "/api/groups/1")
	wantStatus(t, rr, http.StatusBadRequest)
	var resp struct {
		Error string `json:"error"`
	}
	decodeJSON(t, rr, &resp)
	if !strings.Contains(resp.Error, "仍被用户") {
		t.Fatalf("error = %q", resp.Error)
	}
}

func TestNonAdmin403(t *testing.T) {
	st, err := openTestStore(t)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser("reader", "reader123", 2); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	srv := testServerWithStore(t, st)
	cookie := loginAs(t, srv, "reader", "reader123")
	for _, req := range []struct {
		method, path, body string
	}{
		{"GET", "/api/users", ""},
		{"POST", "/api/users", `{"username":"z","password":"123456","group_id":2}`},
		{"GET", "/api/groups", ""},
		{"GET", "/api/users/1/tokens", ""},
	} {
		rr := doReq(t, srv, cookie, req.method, req.path, req.body)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("%s %s: status = %d, want 403 (body=%s)", req.method, req.path, rr.Code, rr.Body.String())
		}
	}
}

func itoa(n int) string {
	return strconv.Itoa(n)
}
