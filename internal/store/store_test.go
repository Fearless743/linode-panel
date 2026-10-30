package store

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"linode-panel/internal/linode"
)

// newTestStore opens a fresh store in a temp dir and returns it with the
// admin plaintext password.
func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	s, pass, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if pass == "" {
		t.Fatal("first Open must return an admin plaintext password")
	}
	return s, pass
}

func TestOpenSeedsDefaults(t *testing.T) {
	s, pass := newTestStore(t)

	groups := s.Groups()
	if len(groups) != 3 {
		t.Fatalf("want 3 seed groups, got %d", len(groups))
	}
	byName := map[string]Group{}
	for _, g := range groups {
		byName[g.Name] = g
	}
	want := map[string][]string{
		"管理员": {PermView, PermPower, PermRebuild, PermScript, PermAdmin},
		"只读":  {PermView},
		"运维":  {PermView, PermPower, PermRebuild, PermScript},
	}
	for name, perms := range want {
		g, ok := byName[name]
		if !ok {
			t.Fatalf("missing seed group %q", name)
		}
		if len(g.Perms) != len(perms) {
			t.Fatalf("group %q perms = %v, want %v", name, g.Perms, perms)
		}
		for i, p := range perms {
			if g.Perms[i] != p {
				t.Fatalf("group %q perms = %v, want %v", name, g.Perms, perms)
			}
		}
	}

	admin, ok := s.UserByName("admin")
	if !ok {
		t.Fatal("seed admin user missing")
	}
	if admin.GroupID != byName["管理员"].ID {
		t.Fatalf("admin group = %d, want %d", admin.GroupID, byName["管理员"].ID)
	}
	if !CheckPasswordHash(admin.PwdHash, pass) {
		t.Fatal("seed admin password does not verify")
	}
}

func TestPasswordHash(t *testing.T) {
	hash, err := HashPassword("secret123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !CheckPasswordHash(hash, "secret123") {
		t.Fatal("correct password rejected")
	}
	if CheckPasswordHash(hash, "wrong") {
		t.Fatal("wrong password accepted")
	}
	if CheckPasswordHash("not-a-hash", "x") {
		t.Fatal("malformed hash accepted")
	}
}

func TestUserCRUD(t *testing.T) {
	s, _ := newTestStore(t)

	// CreateUser seeds a user into the built-in 运维 group.
	u, err := s.CreateUser("alice", "pw", 3)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.Username != "alice" || u.GroupID != 3 || u.ID == 0 {
		t.Fatalf("unexpected created user: %+v", u)
	}
	if !CheckPasswordHash(u.PwdHash, "pw") {
		t.Fatal("created user password does not verify")
	}

	// Duplicate name rejected.
	if _, err := s.CreateUser("alice", "other", 2); err == nil {
		t.Fatal("duplicate username accepted")
	}

	// Lookup.
	if got, ok := s.User(u.ID); !ok || got.Username != "alice" {
		t.Fatalf("User(%d) = %+v, %v", u.ID, got, ok)
	}
	if got, ok := s.UserByName("alice"); !ok || got.ID != u.ID {
		t.Fatalf("UserByName(alice) = %+v, %v", got, ok)
	}
	if _, ok := s.UserByName("nobody"); ok {
		t.Fatal("unknown username found")
	}

	// UpdateUser: move group + change password.
	if err := s.UpdateUser(u.ID, 2, "newpw"); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	got, _ := s.User(u.ID)
	if got.GroupID != 2 {
		t.Fatalf("group after update = %d, want 2", got.GroupID)
	}
	if !CheckPasswordHash(got.PwdHash, "newpw") {
		t.Fatal("updated password does not verify")
	}
	// Empty password keeps existing hash.
	if err := s.UpdateUser(u.ID, 1, ""); err != nil {
		t.Fatalf("UpdateUser(no pw): %v", err)
	}
	got, _ = s.User(u.ID)
	if !CheckPasswordHash(got.PwdHash, "newpw") {
		t.Fatal("empty update changed the password")
	}

	// CheckPassword.
	cu, err := s.CheckPassword("alice", "newpw")
	if err != nil || cu.ID != u.ID {
		t.Fatalf("CheckPassword = %+v, %v", cu, err)
	}
	if _, err := s.CheckPassword("alice", "bad"); err == nil {
		t.Fatal("bad password accepted")
	}
	if _, err := s.CheckPassword("ghost", "pw"); err == nil {
		t.Fatal("unknown user accepted")
	}

	// DeleteUser and confirm removal.
	if err := s.DeleteUser(u.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, ok := s.User(u.ID); ok {
		t.Fatal("deleted user still present")
	}
	if err := s.DeleteUser(u.ID); err == nil {
		t.Fatal("double delete succeeded")
	}
}

func TestTokenCRUD(t *testing.T) {
	s, _ := newTestStore(t)

	tok, err := s.AddToken(1, "main", "linode_pat_123456")
	if err != nil {
		t.Fatalf("AddToken: %v", err)
	}
	if tok.ID == 0 || tok.Label != "main" || tok.Token != "linode_pat_123456" {
		t.Fatalf("unexpected token: %+v", tok)
	}
	if _, err := s.AddToken(9999, "x", "y"); err == nil {
		t.Fatal("AddToken to unknown user accepted")
	}

	toks := s.TokensOf(1)
	if len(toks) != 1 || toks[0].ID != tok.ID {
		t.Fatalf("TokensOf = %+v", toks)
	}
	// TokenPAT returns plaintext.
	pat, err := s.TokenPAT(1, tok.ID)
	if err != nil || pat != "linode_pat_123456" {
		t.Fatalf("TokenPAT = %q, %v", pat, err)
	}
	if _, err := s.TokenPAT(1, 999); err == nil {
		t.Fatal("unknown token id accepted")
	}

	if err := s.DeleteToken(1, tok.ID); err != nil {
		t.Fatalf("DeleteToken: %v", err)
	}
	if got := s.TokensOf(1); len(got) != 0 {
		t.Fatalf("tokens after delete = %+v", got)
	}
	if err := s.DeleteToken(1, tok.ID); err == nil {
		t.Fatal("double delete succeeded")
	}
}

func TestTokenAgentBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, _, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	tok, err := s.AddToken(1, "main", "linode_pat_123456")
	if err != nil {
		t.Fatalf("AddToken: %v", err)
	}
	// New tokens are unbound.
	if got := s.TokensOf(1)[0].AgentID; got != 0 {
		t.Fatalf("new token AgentID = %d, want 0", got)
	}

	// Bind an agent.
	if err := s.SetTokenAgent(1, tok.ID, 42); err != nil {
		t.Fatalf("SetTokenAgent bind: %v", err)
	}
	if got := s.TokensOf(1)[0].AgentID; got != 42 {
		t.Fatalf("after bind AgentID = %d, want 42", got)
	}

	// Unbind.
	if err := s.SetTokenAgent(1, tok.ID, 0); err != nil {
		t.Fatalf("SetTokenAgent unbind: %v", err)
	}
	if got := s.TokensOf(1)[0].AgentID; got != 0 {
		t.Fatalf("after unbind AgentID = %d, want 0", got)
	}

	// Unknown token / wrong user are rejected.
	if err := s.SetTokenAgent(1, 999, 42); err == nil {
		t.Fatal("SetTokenAgent on unknown token accepted")
	}
	if err := s.SetTokenAgent(9999, tok.ID, 42); err == nil {
		t.Fatal("SetTokenAgent for unknown user accepted")
	}
	// Binding persists across reopen (stored in SQLite).
	if err := s.SetTokenAgent(1, tok.ID, 7); err != nil {
		t.Fatalf("SetTokenAgent rebind: %v", err)
	}
	if err := s.db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	s2, _, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.db.Close()
	if got := s2.TokensOf(1)[0].AgentID; got != 7 {
		t.Fatalf("after reopen AgentID = %d, want 7", got)
	}
}

func TestMasked(t *testing.T) {
	cases := map[string]string{
		"":                "****",
		"a":               "****",
		"abcd":            "****",
		"abcdef":          "****cdef",
		"linode_pat_9999": "****9999",
	}
	for in, want := range cases {
		if got := Masked(in); got != want {
			t.Fatalf("Masked(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSelfAgentName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, _, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Default: not renamed yet.
	if got := s.SelfAgentName(); got != "" {
		t.Fatalf("SelfAgentName() = %q, want empty", got)
	}

	if err := s.SetSelfAgentName("机房面板"); err != nil {
		t.Fatalf("SetSelfAgentName: %v", err)
	}
	if got := s.SelfAgentName(); got != "机房面板" {
		t.Fatalf("SelfAgentName() = %q, want 机房面板", got)
	}

	// Persists across reopen.
	if err := s.db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	s2, _, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.db.Close()
	if got := s2.SelfAgentName(); got != "机房面板" {
		t.Fatalf("after reopen SelfAgentName() = %q, want 机房面板", got)
	}
}

func TestGroupCRUD(t *testing.T) {
	s, _ := newTestStore(t)

	// Groups returns copies — mutating the result must not affect the store.
	gs := s.Groups()
	if len(gs) != 3 {
		t.Fatalf("seed groups = %d", len(gs))
	}
	gs[0].Name = "mutated"
	gs[0].Perms = nil
	if got, _ := s.Group(gs[0].ID); got.Name == "mutated" || len(got.Perms) == 0 {
		t.Fatal("Groups() returned a non-copy")
	}

	// CreateGroup.
	g, err := s.CreateGroup("ops", []string{PermView, PermPower})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if _, err := s.CreateGroup("ops", nil); err == nil {
		t.Fatal("duplicate group name accepted")
	}
	if _, err := s.CreateGroup("badperms", []string{"hack"}); err == nil {
		t.Fatal("unknown permission accepted")
	}

	// UpdateGroup.
	if err := s.UpdateGroup(g.ID, "ops2", []string{PermView}); err != nil {
		t.Fatalf("UpdateGroup: %v", err)
	}
	got, _ := s.Group(g.ID)
	if got.Name != "ops2" || len(got.Perms) != 1 || got.Perms[0] != PermView {
		t.Fatalf("group after update = %+v", got)
	}

	// DeleteGroup referenced by a user must fail.
	if err := s.DeleteGroup(1); err == nil {
		t.Fatal("deleting referenced group (admin) succeeded")
	}
	// Unreferenced group deletes fine.
	if err := s.DeleteGroup(g.ID); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	if _, ok := s.Group(g.ID); ok {
		t.Fatal("deleted group still present")
	}
}

func TestPermsOf(t *testing.T) {
	s, _ := newTestStore(t)

	// admin (id 1) is in 管理员 (group 1) = all perms.
	perms := s.PermsOf(1)
	if len(perms) != 5 {
		t.Fatalf("admin perms = %v", perms)
	}
	// New user in 只读 (group 2) = [view].
	u, err := s.CreateUser("bob", "pw", 2)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	perms = s.PermsOf(u.ID)
	if len(perms) != 1 || perms[0] != PermView {
		t.Fatalf("bob perms = %v", perms)
	}
	// Unknown user.
	if got := s.PermsOf(9999); got != nil {
		t.Fatalf("unknown user perms = %v", got)
	}
}

func TestSessionTTL(t *testing.T) {
	s, _ := newTestStore(t)

	cookie := s.NewSession(1, time.Minute)
	if cookie == "" || len(cookie) != 64 {
		t.Fatalf("cookie = %q (len %d)", cookie, len(cookie))
	}
	u, ok := s.SessionUser(cookie)
	if !ok || u.ID != 1 {
		t.Fatalf("SessionUser = %+v, %v", u, ok)
	}

	// Short TTL expires.
	short := s.NewSession(1, 10*time.Millisecond)
	time.Sleep(30 * time.Millisecond)
	if _, ok := s.SessionUser(short); ok {
		t.Fatal("expired session still valid")
	}

	// DropSession removes.
	drop := s.NewSession(1, time.Minute)
	s.DropSession(drop)
	if _, ok := s.SessionUser(drop); ok {
		t.Fatal("dropped session still valid")
	}

	// Unknown cookie.
	if _, ok := s.SessionUser("no-such-cookie"); ok {
		t.Fatal("unknown cookie accepted")
	}
}

func TestDeleteUserDropsSessions(t *testing.T) {
	s, _ := newTestStore(t)
	u, _ := s.CreateUser("carol", "pw", 2)
	cookie := s.NewSession(u.ID, time.Minute)
	if _, ok := s.SessionUser(cookie); !ok {
		t.Fatal("session not created")
	}
	if err := s.DeleteUser(u.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, ok := s.SessionUser(cookie); ok {
		t.Fatal("session survived user deletion")
	}
}

func TestSaveOpenRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, pass, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	u, _ := s.CreateUser("dave", "pw", 2)
	if _, err := s.AddToken(u.ID, "prod", "linode_pat_secret"); err != nil {
		t.Fatalf("AddToken: %v", err)
	}
	if _, err := s.CreateGroup("custom", []string{PermScript, PermAdmin}); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Reopen: tokens, users and groups must persist; no new admin pass.
	s2, pass2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if pass2 != "" {
		t.Fatalf("reopen returned admin password %q, want empty", pass2)
	}
	got, ok := s2.UserByName("dave")
	if !ok {
		t.Fatal("dave missing after reopen")
	}
	toks := s2.TokensOf(got.ID)
	if len(toks) != 1 || toks[0].Label != "prod" || toks[0].Token != "linode_pat_secret" {
		t.Fatalf("tokens after reopen = %+v", toks)
	}
	if pat, err := s2.TokenPAT(got.ID, toks[0].ID); err != nil || pat != "linode_pat_secret" {
		t.Fatalf("TokenPAT after reopen = %q, %v", pat, err)
	}
	if _, ok := s2.UserByName("admin"); !ok {
		t.Fatal("admin missing after reopen")
	}
	if !CheckPasswordHash(mustHash(t, pass), pass) {
		t.Fatal("seed admin hash lost")
	}
	if g, ok := s2.Group(4); !ok || g.Name != "custom" {
		t.Fatalf("custom group after reopen = %+v, %v", g, ok)
	}
}

func TestInstanceScriptCRUD(t *testing.T) {
	s, _ := newTestStore(t)

	// 未配置时不存在。
	if _, ok := s.InstanceScript(42); ok {
		t.Fatal("no script expected for a fresh instance")
	}

	// 配置。
	if err := s.SetInstanceScript(42, "echo a"); err != nil {
		t.Fatalf("SetInstanceScript: %v", err)
	}
	got, ok := s.InstanceScript(42)
	if !ok || got != "echo a" {
		t.Fatalf("InstanceScript = %q, %v", got, ok)
	}

	// 更新覆盖。
	if err := s.SetInstanceScript(42, "echo b"); err != nil {
		t.Fatalf("SetInstanceScript update: %v", err)
	}
	got, _ = s.InstanceScript(42)
	if got != "echo b" {
		t.Fatalf("update failed: %q", got)
	}

	// 列表。
	if ids := s.InstanceScriptIDs(); len(ids) != 1 || ids[0] != 42 {
		t.Fatalf("InstanceScriptIDs = %v", ids)
	}

	// 删除 → 回退。
	if err := s.DeleteInstanceScript(42); err != nil {
		t.Fatalf("DeleteInstanceScript: %v", err)
	}
	if _, ok := s.InstanceScript(42); ok {
		t.Fatal("still configured after delete")
	}
	if ids := s.InstanceScriptIDs(); len(ids) != 0 {
		t.Fatalf("ids after delete = %v", ids)
	}
}

// mustHash hashes p or fails the test.
func mustHash(t *testing.T, p string) string {
	t.Helper()
	h, err := HashPassword(p)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	return h
}

func TestConcurrentAccess(t *testing.T) {
	s, _ := newTestStore(t)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			u, err := s.CreateUser("u", "pw", 2)
			if err != nil {
				// Duplicate username races are expected; only real errors fail.
				if err.Error() == "用户名 u 已存在" {
					return
				}
				t.Errorf("CreateUser: %v", err)
				return
			}
			s.AddToken(u.ID, "t", "pat")
			s.PermsOf(u.ID)
			s.TokensOf(u.ID)
			s.User(u.ID)
			s.Users()
			s.Groups()
			s.Group(1)
			cookie := s.NewSession(u.ID, time.Minute)
			s.SessionUser(cookie)
			s.DropSession(cookie)
			s.Save()
		}(i)
	}
	wg.Wait()
}

func TestInstanceCacheRoundtrip(t *testing.T) {
	s, _ := newTestStore(t)
	insts := []linode.Instance{
		{ID: 1, Label: "web", Status: "running", Region: "us-east", Type: "g6-standard-1",
			IPv4: []string{"1.2.3.4"}, IPv6: "2600::1", Tags: []string{"prod"}, Created: "2024-01-01T00:00:00"},
		{ID: 2, Label: "db", Status: "offline", Tags: nil},
	}
	if err := s.CacheInstances(7, insts); err != nil {
		t.Fatalf("CacheInstances: %v", err)
	}
	got := s.CachedInstances(7)
	if len(got) != 2 {
		t.Fatalf("cached = %d, want 2", len(got))
	}
	first := got[0]
	if first.ID != 1 || first.Label != "web" || first.Status != "running" ||
		first.Region != "us-east" || first.Type != "g6-standard-1" ||
		len(first.IPv4) != 1 || first.IPv4[0] != "1.2.3.4" || first.IPv6 != "2600::1" ||
		len(first.Tags) != 1 || first.Tags[0] != "prod" || first.Created != "2024-01-01T00:00:00" {
		t.Fatalf("unexpected cached instance: %+v", first)
	}
	if got[1].Tags == nil || len(got[1].Tags) != 0 {
		t.Fatalf("nil tags should round-trip to empty slice, got %#v", got[1].Tags)
	}

	// Replace semantics: caching a smaller set drops the missing instance.
	if err := s.CacheInstances(7, []linode.Instance{{ID: 1, Label: "web", Status: "offline"}}); err != nil {
		t.Fatalf("CacheInstances replace: %v", err)
	}
	got = s.CachedInstances(7)
	if len(got) != 1 || got[0].ID != 1 || got[0].Status != "offline" {
		t.Fatalf("after replace = %+v, want only id 1 offline", got)
	}
}

func TestInstanceCacheUpsert(t *testing.T) {
	s, _ := newTestStore(t)
	// Populate a snapshot with two instances.
	if err := s.CacheInstances(7, []linode.Instance{
		{ID: 1, Label: "web", Status: "offline", IPv4: []string{"1.2.3.4"}},
		{ID: 2, Label: "db", Status: "offline"},
	}); err != nil {
		t.Fatal(err)
	}
	// Upsert updates an existing row and leaves the others untouched.
	if err := s.UpsertInstanceCache(7, linode.Instance{ID: 1, Label: "web", Status: "running", IPv4: []string{"5.6.7.8"}}); err != nil {
		t.Fatalf("UpsertInstanceCache: %v", err)
	}
	got := s.CachedInstances(7)
	if len(got) != 2 {
		t.Fatalf("cached = %d, want 2", len(got))
	}
	if got[0].ID != 1 || got[0].Status != "running" || len(got[0].IPv4) != 1 || got[0].IPv4[0] != "5.6.7.8" {
		t.Fatalf("upserted instance = %+v, want id 1 running 5.6.7.8", got[0])
	}
	if got[1].ID != 2 || got[1].Status != "offline" {
		t.Fatalf("untouched instance = %+v, want id 2 offline", got[1])
	}
	// Upsert of a brand-new instance adds a row.
	if err := s.UpsertInstanceCache(7, linode.Instance{ID: 3, Label: "cache", Status: "offline"}); err != nil {
		t.Fatalf("UpsertInstanceCache new: %v", err)
	}
	got = s.CachedInstances(7)
	if len(got) != 3 {
		t.Fatalf("cached = %d, want 3", len(got))
	}
}

func TestInstanceCacheIsolationAndInvalidation(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.CacheInstances(1, []linode.Instance{{ID: 1, Label: "a"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.CacheInstances(2, []linode.Instance{{ID: 9, Label: "b"}}); err != nil {
		t.Fatal(err)
	}
	// Per-token isolation.
	if got := s.CachedInstances(1); len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("token 1 cache = %+v", got)
	}
	// Invalidate only one token.
	if err := s.InvalidateInstanceCache(1); err != nil {
		t.Fatal(err)
	}
	if got := s.CachedInstances(1); len(got) != 0 {
		t.Fatalf("token 1 cache after invalidate = %+v", got)
	}
	if got := s.CachedInstances(2); len(got) != 1 || got[0].ID != 9 {
		t.Fatalf("token 2 cache should be unaffected: %+v", got)
	}
}

func TestDeleteTokenClearsCache(t *testing.T) {
	s, _ := newTestStore(t)
	u, err := s.CreateUser("bob", "bob123", 1)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	tok, err := s.AddToken(u.ID, "main", "pat1")
	if err != nil {
		t.Fatalf("AddToken: %v", err)
	}
	if err := s.CacheInstances(tok.ID, []linode.Instance{{ID: 1, Label: "web"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteToken(u.ID, tok.ID); err != nil {
		t.Fatalf("DeleteToken: %v", err)
	}
	if got := s.CachedInstances(tok.ID); len(got) != 0 {
		t.Fatalf("cache should be cleared after token delete: %+v", got)
	}
}

func TestDeleteUserClearsCache(t *testing.T) {
	s, _ := newTestStore(t)
	u, err := s.CreateUser("carol", "carol123", 1)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	tok, err := s.AddToken(u.ID, "main", "pat1")
	if err != nil {
		t.Fatalf("AddToken: %v", err)
	}
	if err := s.CacheInstances(tok.ID, []linode.Instance{{ID: 1, Label: "web"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(u.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if got := s.CachedInstances(tok.ID); len(got) != 0 {
		t.Fatalf("cache should be cleared after user delete: %+v", got)
	}
}
