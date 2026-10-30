package store

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// AccountGroups 返回某 user 的全部账号组，按 id 升序；每个组的 TokenIDs 升序。
func (s *Store) AccountGroups(userID int) []AccountGroup {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accountGroupsLocked(userID)
}

// AccountGroup 按 id 返回某 user 的一个账号组；不属于该 user 时视作不存在。
func (s *Store) AccountGroup(userID, id int) (AccountGroup, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accountGroupLocked(userID, id)
}

// CreateAccountGroup 为某 user 新建一个空账号组。组名 TrimSpace 后不能为空，
// 且在同一 user 内唯一；返回创建后的 AccountGroup（TokenIDs 为空切片）。
func (s *Store) CreateAccountGroup(userID int, name string) (AccountGroup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name = strings.TrimSpace(name)
	if name == "" {
		return AccountGroup{}, errors.New("组名不能为空")
	}
	if s.accountGroupNameExistsLocked(userID, name) {
		return AccountGroup{}, fmt.Errorf("组名 %s 已存在", name)
	}
	createdAt := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.Exec(
		`INSERT INTO account_groups(user_id, name, created_at) VALUES(?,?,?)`,
		userID, name, createdAt)
	if err != nil {
		return AccountGroup{}, err
	}
	id, _ := res.LastInsertId()
	return AccountGroup{
		ID:        int(id),
		UserID:    userID,
		Name:      name,
		TokenIDs:  []int{},
		CreatedAt: createdAt,
	}, nil
}

// UpdateAccountGroup 重命名某 user 的一个账号组。组名 TrimSpace 后不能为空，
// 且在同一 user 内唯一（可以改成自己当前的名字）。
func (s *Store) UpdateAccountGroup(userID, id int, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accountGroupLocked(userID, id); !ok {
		return fmt.Errorf("账号组 %d 不存在", id)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("组名不能为空")
	}
	if s.accountGroupNameExistsExceptLocked(userID, name, id) {
		return fmt.Errorf("组名 %s 已存在", name)
	}
	_, err := s.db.Exec(`UPDATE account_groups SET name=? WHERE id=? AND user_id=?`, name, id, userID)
	return err
}

// DeleteAccountGroup 删除某 user 的一个账号组，并连同删除其成员行。
// 组不存在（或不属于该 user）时返回错误。
func (s *Store) DeleteAccountGroup(userID, id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accountGroupLocked(userID, id); !ok {
		return fmt.Errorf("账号组 %d 不存在", id)
	}
	// SQLite 外键默认可能未开启，这里手动清理成员行，放在同一事务里保证一致。
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM account_group_members WHERE group_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM account_groups WHERE id=? AND user_id=?`, id, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// AddAccountGroupMember 把一个账号(Token)加入账号组。组必须属于该 user，
// token 也必须属于同一 user；重复添加幂等（不报错）。
func (s *Store) AddAccountGroupMember(userID, groupID, tokenID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accountGroupLocked(userID, groupID); !ok {
		return fmt.Errorf("账号组 %d 不存在", groupID)
	}
	if !s.tokenBelongsLocked(userID, tokenID) {
		return fmt.Errorf("令牌 %d 不存在", tokenID)
	}
	_, err := s.db.Exec(
		`INSERT INTO account_group_members(group_id, token_id) VALUES(?,?)
		 ON CONFLICT(group_id, token_id) DO NOTHING`, groupID, tokenID)
	return err
}

// RemoveAccountGroupMember 把一个账号(Token)移出账号组。组必须属于该 user；
// 移出非成员是幂等的（不报错）。
func (s *Store) RemoveAccountGroupMember(userID, groupID, tokenID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accountGroupLocked(userID, groupID); !ok {
		return fmt.Errorf("账号组 %d 不存在", groupID)
	}
	_, err := s.db.Exec(`DELETE FROM account_group_members WHERE group_id=? AND token_id=?`, groupID, tokenID)
	return err
}

// AccountGroupsOfToken 反查某账号(Token)所属的全部账号组，按 id 升序。
func (s *Store) AccountGroupsOfToken(userID, tokenID int) []AccountGroup {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(
		`SELECT g.id, g.user_id, g.name, g.created_at
		   FROM account_groups g
		   JOIN account_group_members m ON m.group_id = g.id
		  WHERE g.user_id=? AND m.token_id=?
		  ORDER BY g.id`, userID, tokenID)
	if err != nil {
		return nil
	}
	var out []AccountGroup
	for rows.Next() {
		var g AccountGroup
		if err := rows.Scan(&g.ID, &g.UserID, &g.Name, &g.CreatedAt); err != nil {
			rows.Close()
			return nil
		}
		out = append(out, g)
	}
	rows.Close()
	// 单条 SQLite 连接：先关闭 rows 再逐个补齐成员，否则嵌套查询会被阻塞。
	for i := range out {
		out[i].TokenIDs = s.accountGroupMembersLocked(out[i].ID)
	}
	return out
}

// --- internal helpers (callers hold s.mu) ---

func (s *Store) accountGroupLocked(userID, id int) (AccountGroup, bool) {
	row := s.db.QueryRow(
		`SELECT id, user_id, name, created_at FROM account_groups WHERE id=? AND user_id=?`, id, userID)
	var g AccountGroup
	if err := row.Scan(&g.ID, &g.UserID, &g.Name, &g.CreatedAt); err != nil {
		return AccountGroup{}, false
	}
	g.TokenIDs = s.accountGroupMembersLocked(g.ID)
	return g, true
}

func (s *Store) accountGroupsLocked(userID int) []AccountGroup {
	rows, err := s.db.Query(
		`SELECT id, user_id, name, created_at FROM account_groups WHERE user_id=? ORDER BY id`, userID)
	if err != nil {
		return nil
	}
	var out []AccountGroup
	for rows.Next() {
		var g AccountGroup
		if err := rows.Scan(&g.ID, &g.UserID, &g.Name, &g.CreatedAt); err != nil {
			rows.Close()
			return nil
		}
		out = append(out, g)
	}
	rows.Close()
	// 单条 SQLite 连接：先关闭 rows 再逐个补齐成员。
	for i := range out {
		out[i].TokenIDs = s.accountGroupMembersLocked(out[i].ID)
	}
	return out
}

func (s *Store) accountGroupMembersLocked(groupID int) []int {
	rows, err := s.db.Query(
		`SELECT token_id FROM account_group_members WHERE group_id=? ORDER BY token_id`, groupID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var tid int
		if err := rows.Scan(&tid); err != nil {
			break
		}
		out = append(out, tid)
	}
	return out
}

func (s *Store) accountGroupNameExistsLocked(userID int, name string) bool {
	return s.accountGroupNameExistsExceptLocked(userID, name, 0)
}

// accountGroupNameExistsExceptLocked 判断某 user 内是否已存在该组名（忽略
// exceptID 指向的组，用于改名时不与自己冲突）。
func (s *Store) accountGroupNameExistsExceptLocked(userID int, name string, exceptID int) bool {
	var n int
	_ = s.db.QueryRow(
		`SELECT COUNT(*) FROM account_groups WHERE user_id=? AND name=? AND id<>?`,
		userID, name, exceptID).Scan(&n)
	return n > 0
}

// tokenBelongsLocked 判断 token 是否存在且属于该 user。
func (s *Store) tokenBelongsLocked(userID, tokenID int) bool {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM tokens WHERE id=? AND user_id=?`, tokenID, userID).Scan(&n)
	return n > 0
}
