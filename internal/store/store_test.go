package store

import (
	"database/sql"
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

func TestInstanceCacheRemove(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.CacheInstances(7, []linode.Instance{
		{ID: 1, Label: "web"},
		{ID: 2, Label: "db"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CacheInstances(8, []linode.Instance{{ID: 2, Label: "other"}}); err != nil {
		t.Fatal(err)
	}
	// Remove one instance from one token only.
	if err := s.RemoveInstanceCache(7, 2); err != nil {
		t.Fatalf("RemoveInstanceCache: %v", err)
	}
	got := s.CachedInstances(7)
	if len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("token 7 cache = %+v, want only instance 1", got)
	}
	if got := s.CachedInstances(8); len(got) != 1 || got[0].ID != 2 {
		t.Fatalf("token 8 cache should be unaffected: %+v", got)
	}
	// Removing a missing instance is a no-op.
	if err := s.RemoveInstanceCache(7, 999); err != nil {
		t.Fatalf("RemoveInstanceCache missing: %v", err)
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
	// Invalidate only one token: rows (and their stable ids) stay, but the
	// snapshot is marked stale so the list refetches.
	if err := s.InvalidateInstanceCache(1); err != nil {
		t.Fatal(err)
	}
	if got := s.CachedInstances(1); len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("token 1 cache after invalidate = %+v, want the row kept", got)
	}
	if !s.InstanceCacheStale(1) {
		t.Fatal("token 1 cache should be stale after invalidate")
	}
	if s.InstanceCacheStale(2) {
		t.Fatal("token 2 cache should not be stale")
	}
	if got := s.CachedInstances(2); len(got) != 1 || got[0].ID != 9 {
		t.Fatalf("token 2 cache should be unaffected: %+v", got)
	}
	// Refreshing the snapshot clears the stale marker.
	if err := s.CacheInstances(1, []linode.Instance{{ID: 1, Label: "a", Status: "running"}}); err != nil {
		t.Fatal(err)
	}
	if s.InstanceCacheStale(1) {
		t.Fatal("stale marker should clear after a successful refresh")
	}
}

// TestInstanceCacheIDStableAcrossRefresh verifies that refreshing a token's
// snapshot keeps each instance's stable auto-increment id instead of minting a
// new one (configuration is keyed by that id).
func TestInstanceCacheIDStableAcrossRefresh(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.CacheInstances(7, []linode.Instance{{ID: 1, Label: "web"}, {ID: 2, Label: "db"}}); err != nil {
		t.Fatal(err)
	}
	var idBefore int
	if err := s.db.QueryRow(`SELECT id FROM instance_cache WHERE token_id=7 AND instance_id=1`).Scan(&idBefore); err != nil {
		t.Fatal(err)
	}
	if err := s.CacheInstances(7, []linode.Instance{{ID: 1, Label: "web2"}, {ID: 2, Label: "db"}}); err != nil {
		t.Fatal(err)
	}
	var idAfter int
	if err := s.db.QueryRow(`SELECT id FROM instance_cache WHERE token_id=7 AND instance_id=1`).Scan(&idAfter); err != nil {
		t.Fatal(err)
	}
	if idBefore != idAfter {
		t.Fatalf("cache id changed across refresh: %d -> %d", idBefore, idAfter)
	}
}

// TestRepointInstanceCacheInheritsConfig is the core rebuild-inheritance test:
// scripts / auto-rebuild / failover configuration keyed by the stable cache id
// follows the instance when its Linode id changes.
func TestRepointInstanceCacheInheritsConfig(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.CacheInstances(7, []linode.Instance{{ID: 42, Label: "web"}}); err != nil {
		t.Fatal(err)
	}
	var cacheID int
	if err := s.db.QueryRow(`SELECT id FROM instance_cache WHERE token_id=7 AND instance_id=42`).Scan(&cacheID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetInstanceScript(42, "echo hi"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetInstanceRootPassword(42, "Secret123!"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAutoRebuild(AutoRebuild{
		InstanceID: 42, UserID: 1, TokenID: 7, Enabled: true, Port: 2222, Threshold: 2, IntervalSec: 60,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFailover(Failover{
		InstanceID: 42, UserID: 1, GroupID: 3, TokenID: 7, Enabled: true, Threshold: 2, IntervalSec: 60,
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.RepointInstanceCache(42, 99); err != nil {
		t.Fatalf("RepointInstanceCache: %v", err)
	}

	// The cache row keeps its id and now points at the new instance.
	var gotID, gotInstance int
	if err := s.db.QueryRow(`SELECT id, instance_id FROM instance_cache WHERE token_id=7`).Scan(&gotID, &gotInstance); err != nil {
		t.Fatal(err)
	}
	if gotID != cacheID || gotInstance != 99 {
		t.Fatalf("cache row = (id=%d, instance_id=%d), want (id=%d, 99)", gotID, gotInstance, cacheID)
	}
	// All three configurations resolve under the new instance id.
	if script, ok := s.InstanceScript(99); !ok || script != "echo hi" {
		t.Fatalf("InstanceScript(99) = %q, %v", script, ok)
	}
	if pass, ok := s.InstanceRootPassword(99); !ok || pass != "Secret123!" {
		t.Fatalf("InstanceRootPassword(99) = %q, %v", pass, ok)
	}
	if cfg, ok := s.AutoRebuild(99); !ok || !cfg.Enabled || cfg.Port != 2222 {
		t.Fatalf("AutoRebuild(99) = %+v, %v", cfg, ok)
	}
	if cfg, ok := s.Failover(99); !ok || !cfg.Enabled || cfg.GroupID != 3 {
		t.Fatalf("Failover(99) = %+v, %v", cfg, ok)
	}
	if _, ok := s.InstanceScript(42); ok {
		t.Fatal("script should no longer resolve under the old instance id")
	}

	// A subsequent snapshot refresh must not change the stable id.
	if err := s.CacheInstances(7, []linode.Instance{{ID: 99, Label: "web"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT id FROM instance_cache WHERE token_id=7`).Scan(&gotID); err != nil {
		t.Fatal(err)
	}
	if gotID != cacheID {
		t.Fatalf("cache id changed after refresh: %d -> %d", cacheID, gotID)
	}
}

// TestMigrateLegacyInstanceConfigToCacheID builds an old-schema database
// (config tables keyed by Linode instance id, instance_cache without the
// stable id) and verifies Open upgrades it without losing configuration.
func TestMigrateLegacyInstanceConfigToCacheID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := `
CREATE TABLE groups (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE);
CREATE TABLE instance_cache (
	token_id INTEGER NOT NULL, instance_id INTEGER NOT NULL,
	label TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT '',
	region TEXT NOT NULL DEFAULT '', type TEXT NOT NULL DEFAULT '',
	image TEXT NOT NULL DEFAULT '', ipv4 TEXT NOT NULL DEFAULT '[]',
	ipv6 TEXT NOT NULL DEFAULT '', tags TEXT NOT NULL DEFAULT '[]',
	created TEXT NOT NULL DEFAULT '', updated TEXT NOT NULL DEFAULT '',
	fetched_at TEXT NOT NULL, PRIMARY KEY (token_id, instance_id));
CREATE TABLE instance_scripts (instance_id INTEGER PRIMARY KEY, script TEXT NOT NULL);
CREATE TABLE instance_auto_rebuild (
	instance_id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL DEFAULT 0,
	token_id INTEGER NOT NULL DEFAULT 0, enabled INTEGER NOT NULL DEFAULT 0,
	port INTEGER NOT NULL DEFAULT 22, threshold INTEGER NOT NULL DEFAULT 3,
	interval_sec INTEGER NOT NULL DEFAULT 300, fail_count INTEGER NOT NULL DEFAULT 0,
	last_status TEXT NOT NULL DEFAULT '', last_detail TEXT NOT NULL DEFAULT '',
	last_check_at TEXT NOT NULL DEFAULT '', last_blocked_at TEXT NOT NULL DEFAULT '',
	last_task_id TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL DEFAULT '');
CREATE TABLE instance_failover (
	instance_id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL DEFAULT 0,
	group_id INTEGER NOT NULL DEFAULT 0, token_id INTEGER NOT NULL DEFAULT 0,
	enabled INTEGER NOT NULL DEFAULT 0, threshold INTEGER NOT NULL DEFAULT 3,
	interval_sec INTEGER NOT NULL DEFAULT 300, fail_count INTEGER NOT NULL DEFAULT 0,
	last_status TEXT NOT NULL DEFAULT '', last_detail TEXT NOT NULL DEFAULT '',
	last_check_at TEXT NOT NULL DEFAULT '', last_task_id TEXT NOT NULL DEFAULT '',
	replaced_by INTEGER NOT NULL DEFAULT 0, updated_at TEXT NOT NULL DEFAULT '');
INSERT INTO groups(name) VALUES('legacy');
INSERT INTO instance_cache(token_id, instance_id, label, fetched_at) VALUES(7, 42, 'web', '2026-01-01T00:00:00Z');
INSERT INTO instance_scripts(instance_id, script) VALUES(42, 'echo inherited');
INSERT INTO instance_auto_rebuild(instance_id, user_id, token_id, enabled, port) VALUES(42, 1, 7, 1, 22);
INSERT INTO instance_failover(instance_id, user_id, group_id, token_id, enabled) VALUES(42, 1, 3, 7, 1);
`
	if _, err := db.Exec(legacy); err != nil {
		t.Fatalf("seed legacy schema: %v", err)
	}
	db.Close()

	s, _, err := Open(path)
	if err != nil {
		t.Fatalf("Open legacy db: %v", err)
	}
	if script, ok := s.InstanceScript(42); !ok || script != "echo inherited" {
		t.Fatalf("InstanceScript(42) = %q, %v", script, ok)
	}
	if cfg, ok := s.AutoRebuild(42); !ok || !cfg.Enabled {
		t.Fatalf("AutoRebuild(42) = %+v, %v", cfg, ok)
	}
	if cfg, ok := s.Failover(42); !ok || !cfg.Enabled || cfg.GroupID != 3 {
		t.Fatalf("Failover(42) = %+v, %v", cfg, ok)
	}
	// The migrated configuration is inherited by a rebuilt instance id.
	if err := s.RepointInstanceCache(42, 99); err != nil {
		t.Fatal(err)
	}
	if script, ok := s.InstanceScript(99); !ok || script != "echo inherited" {
		t.Fatalf("InstanceScript(99) = %q, %v", script, ok)
	}
	if cfg, ok := s.AutoRebuild(99); !ok || !cfg.Enabled {
		t.Fatalf("AutoRebuild(99) = %+v, %v", cfg, ok)
	}
	if cfg, ok := s.Failover(99); !ok || !cfg.Enabled {
		t.Fatalf("Failover(99) = %+v, %v", cfg, ok)
	}
}

// TestRepointInstanceCacheMergesDuplicateRow covers the race where the new
// instance was already cached (e.g. a list refresh ran during the rebuild):
// the repoint must merge into the old row instead of failing on the unique
// (token_id, instance_id) constraint.
func TestRepointInstanceCacheMergesDuplicateRow(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.CacheInstances(7, []linode.Instance{{ID: 42, Label: "old"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetInstanceScript(42, "old script"); err != nil {
		t.Fatal(err)
	}
	var oldCacheID int
	if err := s.db.QueryRow(`SELECT id FROM instance_cache WHERE token_id=7 AND instance_id=42`).Scan(&oldCacheID); err != nil {
		t.Fatal(err)
	}
	// A refresh already cached the replacement under the same token.
	if err := s.UpsertInstanceCache(7, linode.Instance{ID: 99, Label: "new"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RepointInstanceCache(42, 99); err != nil {
		t.Fatalf("RepointInstanceCache with duplicate: %v", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM instance_cache WHERE token_id=7`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("cache rows = %d, want 1 after merge", n)
	}
	var gotID, gotInstance int
	if err := s.db.QueryRow(`SELECT id, instance_id FROM instance_cache WHERE token_id=7`).Scan(&gotID, &gotInstance); err != nil {
		t.Fatal(err)
	}
	if gotID != oldCacheID || gotInstance != 99 {
		t.Fatalf("row = (id=%d, instance_id=%d), want (id=%d, 99)", gotID, gotInstance, oldCacheID)
	}
	if script, ok := s.InstanceScript(99); !ok || script != "old script" {
		t.Fatalf("InstanceScript(99) = %q, %v", script, ok)
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
