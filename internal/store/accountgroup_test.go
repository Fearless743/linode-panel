package store

import (
	"testing"
	"time"
)

// newTestUserToken 建一个用户及其一个账号(Token)，供账号组测试使用。
func newTestUserToken(t *testing.T, s *Store, username string) (User, Token) {
	t.Helper()
	u, err := s.CreateUser(username, "pw", 2)
	if err != nil {
		t.Fatalf("CreateUser(%s): %v", username, err)
	}
	tok, err := s.AddToken(u.ID, username+"-main", "pat-"+username)
	if err != nil {
		t.Fatalf("AddToken(%s): %v", username, err)
	}
	return u, tok
}

func TestCreateAccountGroup(t *testing.T) {
	s, _ := newTestStore(t)
	u, _ := newTestUserToken(t, s, "alice")

	g, err := s.CreateAccountGroup(u.ID, "  生产  ")
	if err != nil {
		t.Fatalf("CreateAccountGroup: %v", err)
	}
	if g.ID == 0 {
		t.Fatal("created group has zero id")
	}
	if g.UserID != u.ID {
		t.Fatalf("UserID = %d, want %d", g.UserID, u.ID)
	}
	if g.Name != "生产" {
		t.Fatalf("Name = %q, want trimmed %q", g.Name, "生产")
	}
	if g.TokenIDs == nil {
		t.Fatal("TokenIDs must be an empty slice, not nil")
	}
	if len(g.TokenIDs) != 0 {
		t.Fatalf("new group TokenIDs = %v, want empty", g.TokenIDs)
	}
	if _, err := time.Parse(time.RFC3339, g.CreatedAt); err != nil {
		t.Fatalf("CreatedAt %q is not RFC3339: %v", g.CreatedAt, err)
	}

	// 读回。
	got, ok := s.AccountGroup(u.ID, g.ID)
	if !ok || got.Name != "生产" {
		t.Fatalf("AccountGroup = %+v, %v", got, ok)
	}
}

func TestCreateAccountGroupNameValidation(t *testing.T) {
	s, _ := newTestStore(t)
	u, _ := newTestUserToken(t, s, "alice")
	other, _ := newTestUserToken(t, s, "bob")

	if _, err := s.CreateAccountGroup(u.ID, "生产"); err != nil {
		t.Fatalf("seed group: %v", err)
	}

	cases := []struct {
		name    string
		userID  int
		wantErr bool
	}{
		{"", u.ID, true},
		{"   ", u.ID, true},
		{"生产", u.ID, true},      // 同 user 内重复
		{" 生产 ", u.ID, true},    // 去空格后重复
		{"测试", u.ID, false},     // 新名字
		{"生产", other.ID, false}, // 另一个 user 可以用同名
	}
	for _, c := range cases {
		_, err := s.CreateAccountGroup(c.userID, c.name)
		if c.wantErr && err == nil {
			t.Errorf("CreateAccountGroup(%q) = nil error, want error", c.name)
		}
		if !c.wantErr && err != nil {
			t.Errorf("CreateAccountGroup(%q) = %v, want nil", c.name, err)
		}
	}
}

func TestUpdateAndDeleteAccountGroup(t *testing.T) {
	s, _ := newTestStore(t)
	u, tok := newTestUserToken(t, s, "alice")
	other, _ := newTestUserToken(t, s, "bob")

	g1, err := s.CreateAccountGroup(u.ID, "生产")
	if err != nil {
		t.Fatalf("CreateAccountGroup: %v", err)
	}
	g2, _ := s.CreateAccountGroup(u.ID, "备用")

	// 改名。
	if err := s.UpdateAccountGroup(u.ID, g1.ID, "主生产"); err != nil {
		t.Fatalf("UpdateAccountGroup: %v", err)
	}
	if got, _ := s.AccountGroup(u.ID, g1.ID); got.Name != "主生产" {
		t.Fatalf("after rename Name = %q", got.Name)
	}
	// 改成自己当前的名字是允许的。
	if err := s.UpdateAccountGroup(u.ID, g1.ID, "主生产"); err != nil {
		t.Fatalf("rename to same name: %v", err)
	}
	// 改成同 user 内已存在的名字失败。
	if err := s.UpdateAccountGroup(u.ID, g1.ID, "备用"); err == nil {
		t.Fatal("rename to duplicate accepted")
	}
	// 空名字失败。
	if err := s.UpdateAccountGroup(u.ID, g1.ID, "   "); err == nil {
		t.Fatal("rename to empty accepted")
	}
	// 越权：另一个 user 改不到。
	if err := s.UpdateAccountGroup(other.ID, g1.ID, "偷改"); err == nil {
		t.Fatal("cross-user update accepted")
	}
	// 不存在的组。
	if err := s.UpdateAccountGroup(u.ID, 9999, "x"); err == nil {
		t.Fatal("update of unknown group accepted")
	}

	// 加成员后删组，成员行应级联删除。
	if err := s.AddAccountGroupMember(u.ID, g1.ID, tok.ID); err != nil {
		t.Fatalf("AddAccountGroupMember: %v", err)
	}
	if err := s.DeleteAccountGroup(u.ID, g1.ID); err != nil {
		t.Fatalf("DeleteAccountGroup: %v", err)
	}
	if _, ok := s.AccountGroup(u.ID, g1.ID); ok {
		t.Fatal("deleted group still readable")
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM account_group_members WHERE group_id=?`, g1.ID).Scan(&n); err != nil {
		t.Fatalf("count members: %v", err)
	}
	if n != 0 {
		t.Fatalf("member rows not cascaded: %d left", n)
	}
	// 越权删除失败。
	if err := s.DeleteAccountGroup(other.ID, g2.ID); err == nil {
		t.Fatal("cross-user delete accepted")
	}
	// 删不存在的组失败。
	if err := s.DeleteAccountGroup(u.ID, g2.ID); err != nil {
		t.Fatalf("delete own group: %v", err)
	}
	if err := s.DeleteAccountGroup(u.ID, g2.ID); err == nil {
		t.Fatal("delete of already-deleted group accepted")
	}
}

func TestAccountGroupMembers(t *testing.T) {
	s, _ := newTestStore(t)
	alice, t1 := newTestUserToken(t, s, "alice")
	bob, bobTok := newTestUserToken(t, s, "bob")

	// 再给 alice 加一个 token，id 比 t1 大。
	t2, err := s.AddToken(alice.ID, "second", "pat-second")
	if err != nil {
		t.Fatalf("AddToken: %v", err)
	}

	g, _ := s.CreateAccountGroup(alice.ID, "生产")

	// 乱序加入，读回应按 token_id 升序。
	if err := s.AddAccountGroupMember(alice.ID, g.ID, t2.ID); err != nil {
		t.Fatalf("add t2: %v", err)
	}
	if err := s.AddAccountGroupMember(alice.ID, g.ID, t1.ID); err != nil {
		t.Fatalf("add t1: %v", err)
	}
	// 重复添加幂等。
	if err := s.AddAccountGroupMember(alice.ID, g.ID, t1.ID); err != nil {
		t.Fatalf("duplicate add must be idempotent: %v", err)
	}

	got, _ := s.AccountGroup(alice.ID, g.ID)
	want := []int{t1.ID, t2.ID}
	if len(got.TokenIDs) != len(want) || got.TokenIDs[0] != want[0] || got.TokenIDs[1] != want[1] {
		t.Fatalf("TokenIDs = %v, want %v (ascending)", got.TokenIDs, want)
	}

	// 校验类错误（表驱动）。
	cases := []struct {
		desc    string
		userID  int
		groupID int
		tokenID int
	}{
		{"组不存在", alice.ID, 9999, t1.ID},
		{"组属于他人", bob.ID, g.ID, t1.ID},
		{"token 不存在", alice.ID, g.ID, 9999},
		{"token 属于他人", alice.ID, g.ID, bobTok.ID},
	}
	for _, c := range cases {
		if err := s.AddAccountGroupMember(c.userID, c.groupID, c.tokenID); err == nil {
			t.Errorf("AddAccountGroupMember(%s) = nil error, want error", c.desc)
		}
	}

	// 移出成员。
	if err := s.RemoveAccountGroupMember(alice.ID, g.ID, t2.ID); err != nil {
		t.Fatalf("RemoveAccountGroupMember: %v", err)
	}
	got, _ = s.AccountGroup(alice.ID, g.ID)
	if len(got.TokenIDs) != 1 || got.TokenIDs[0] != t1.ID {
		t.Fatalf("after remove TokenIDs = %v, want [%d]", got.TokenIDs, t1.ID)
	}
	// 再移出同一成员幂等。
	if err := s.RemoveAccountGroupMember(alice.ID, g.ID, t2.ID); err != nil {
		t.Fatalf("remove non-member must be idempotent: %v", err)
	}
	// 越权移出失败。
	if err := s.RemoveAccountGroupMember(bob.ID, g.ID, t1.ID); err == nil {
		t.Fatal("cross-user remove accepted")
	}
	// 组不存在失败。
	if err := s.RemoveAccountGroupMember(alice.ID, 9999, t1.ID); err == nil {
		t.Fatal("remove from unknown group accepted")
	}
}

func TestAccountGroupsOfToken(t *testing.T) {
	s, _ := newTestStore(t)
	alice, t1 := newTestUserToken(t, s, "alice")
	bob, bobTok := newTestUserToken(t, s, "bob")

	g1, _ := s.CreateAccountGroup(alice.ID, "生产")
	g2, _ := s.CreateAccountGroup(alice.ID, "备用")
	g3, _ := s.CreateAccountGroup(bob.ID, "bob组")

	for _, g := range []AccountGroup{g1, g2} {
		if err := s.AddAccountGroupMember(alice.ID, g.ID, t1.ID); err != nil {
			t.Fatalf("add to %s: %v", g.Name, err)
		}
	}
	if err := s.AddAccountGroupMember(bob.ID, g3.ID, bobTok.ID); err != nil {
		t.Fatalf("add bob token: %v", err)
	}

	got := s.AccountGroupsOfToken(alice.ID, t1.ID)
	wantIDs := []int{g1.ID, g2.ID} // 按组 id 升序
	if len(got) != len(wantIDs) {
		t.Fatalf("AccountGroupsOfToken = %d groups, want %d", len(got), len(wantIDs))
	}
	for i, id := range wantIDs {
		if got[i].ID != id {
			t.Fatalf("group[%d].ID = %d, want %d", i, got[i].ID, id)
		}
	}
	// 不属于该 user 的 token：越权反查为空。
	if res := s.AccountGroupsOfToken(alice.ID, bobTok.ID); len(res) != 0 {
		t.Fatalf("cross-user token lookup = %v, want empty", res)
	}
	// 没有任何组的 token 反查为空。
	t2, _ := s.AddToken(alice.ID, "lonely", "pat-lonely")
	if res := s.AccountGroupsOfToken(alice.ID, t2.ID); len(res) != 0 {
		t.Fatalf("token with no groups = %v, want empty", res)
	}
}

func TestAccountGroupsOrderingAndIsolation(t *testing.T) {
	s, _ := newTestStore(t)
	alice, _ := newTestUserToken(t, s, "alice")
	bob, _ := newTestUserToken(t, s, "bob")

	var ids []int
	for _, name := range []string{"a", "b", "c"} {
		g, err := s.CreateAccountGroup(alice.ID, name)
		if err != nil {
			t.Fatalf("CreateAccountGroup(%s): %v", name, err)
		}
		ids = append(ids, g.ID)
	}
	if _, err := s.CreateAccountGroup(bob.ID, "x"); err != nil {
		t.Fatalf("CreateAccountGroup(bob): %v", err)
	}

	got := s.AccountGroups(alice.ID)
	if len(got) != 3 {
		t.Fatalf("AccountGroups(alice) = %d, want 3 (must not leak bob's)", len(got))
	}
	for i, id := range ids {
		if got[i].ID != id {
			t.Fatalf("AccountGroups[%d].ID = %d, want %d (ascending)", i, got[i].ID, id)
		}
	}
	if b := s.AccountGroups(bob.ID); len(b) != 1 {
		t.Fatalf("AccountGroups(bob) = %d, want 1", len(b))
	}
	// 另一个 user 看不到 alice 的组。
	if _, ok := s.AccountGroup(bob.ID, ids[0]); ok {
		t.Fatal("bob can read alice's group")
	}
}
