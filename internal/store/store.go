package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"linode-panel/internal/linode"
)

// Store persists users, groups, tokens, and agent metadata in a SQLite
// database, and keeps login sessions in memory.
type Store struct {
	mu       sync.Mutex
	db       *sql.DB
	sessions map[string]session
}

// session is an in-memory login session keyed by its cookie value.
type session struct {
	userID    int
	expiresAt time.Time
}

// Open opens (or creates) the SQLite database at path and ensures the
// schema exists. On a brand-new database it seeds the built-in groups and
// the admin user, returning the generated admin plaintext password (empty
// when the database already existed). The database file is chmod 0600.
func Open(path string) (*Store, string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, "", fmt.Errorf("创建数据库目录: %w", err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, "", fmt.Errorf("打开数据库: %w", err)
	}
	// SQLite allows a single writer; a single connection avoids SQLITE_BUSY
	// and keeps our read-modify-write sequences atomic.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, "", fmt.Errorf("连接数据库 %s: %w", path, err)
	}
	s := &Store{db: db, sessions: make(map[string]session)}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, "", err
	}
	// Like the old state file, the database must be owner-readable only.
	// Ignore errors for non-regular paths (e.g. ":memory:").
	_ = os.Chmod(path, 0o600)

	empty, err := s.isEmpty()
	if err != nil {
		db.Close()
		return nil, "", err
	}
	if empty {
		return s.seed()
	}
	return s, "", nil
}

// migrate creates the schema when it does not yet exist.
func (s *Store) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS groups (
	id   INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS group_perms (
	group_id INTEGER NOT NULL,
	perm     TEXT NOT NULL,
	PRIMARY KEY (group_id, perm)
);
CREATE TABLE IF NOT EXISTS users (
	id       INTEGER PRIMARY KEY AUTOINCREMENT,
	username TEXT NOT NULL UNIQUE,
	pwd_hash TEXT NOT NULL,
	group_id INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS tokens (
	id       INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id  INTEGER NOT NULL,
	label    TEXT NOT NULL,
	token    TEXT NOT NULL,
	agent_id INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS instance_scripts (
	instance_id INTEGER PRIMARY KEY,
	script      TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS instance_cache (
	token_id    INTEGER NOT NULL,
	instance_id INTEGER NOT NULL,
	label       TEXT NOT NULL DEFAULT '',
	status      TEXT NOT NULL DEFAULT '',
	region      TEXT NOT NULL DEFAULT '',
	type        TEXT NOT NULL DEFAULT '',
	image       TEXT NOT NULL DEFAULT '',
	ipv4        TEXT NOT NULL DEFAULT '[]',
	ipv6        TEXT NOT NULL DEFAULT '',
	tags        TEXT NOT NULL DEFAULT '[]',
	created     TEXT NOT NULL DEFAULT '',
	updated     TEXT NOT NULL DEFAULT '',
	fetched_at  TEXT NOT NULL,
	PRIMARY KEY (token_id, instance_id)
);`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("初始化表结构: %w", err)
	}
	// 旧库的 tokens 表缺少 agent_id 列（账号绑定 agent 功能），需要补列。
	if err := s.ensureTokenAgentColumn(); err != nil {
		return fmt.Errorf("迁移 tokens 表: %w", err)
	}
	return nil
}

// ensureTokenAgentColumn adds the agent_id column to an existing tokens
// table created before the account-agent binding feature. New databases get
// the column from the schema above; this is only a no-op guard for old ones.
func (s *Store) ensureTokenAgentColumn() error {
	has, err := s.columnExists("tokens", "agent_id")
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	_, err = s.db.Exec(`ALTER TABLE tokens ADD COLUMN agent_id INTEGER NOT NULL DEFAULT 0`)
	return err
}

// columnExists reports whether table has a column named col.
func (s *Store) columnExists(table, col string) (bool, error) {
	rows, err := s.db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid     int
			name    string
			typ     string
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == col {
			return true, nil
		}
	}
	return false, rows.Err()
}

// isEmpty reports whether the database holds no groups (i.e. is unseeded).
func (s *Store) isEmpty() (bool, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM groups`).Scan(&n); err != nil {
		return false, fmt.Errorf("读取权限组数量: %w", err)
	}
	return n == 0, nil
}

// seed initializes a brand-new database with the built-in groups and the
// admin user, returning the generated admin plaintext password.
func (s *Store) seed() (*Store, string, error) {
	adminPass := randomHex(16)
	hash, err := bcrypt.GenerateFromPassword([]byte(adminPass), bcrypt.DefaultCost)
	if err != nil {
		return nil, "", err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()

	seeds := []struct {
		name  string
		perms []string
	}{
		{"管理员", []string{PermView, PermPower, PermRebuild, PermScript, PermAdmin}},
		{"只读", []string{PermView}},
		{"运维", []string{PermView, PermPower, PermRebuild, PermScript}},
	}
	var adminGroupID int64
	for i, g := range seeds {
		res, err := tx.Exec(`INSERT INTO groups(name) VALUES(?)`, g.name)
		if err != nil {
			return nil, "", err
		}
		gid, _ := res.LastInsertId()
		for _, p := range g.perms {
			if _, err := tx.Exec(`INSERT INTO group_perms(group_id, perm) VALUES(?,?)`, gid, p); err != nil {
				return nil, "", err
			}
		}
		if i == 0 {
			adminGroupID = gid
		}
	}
	if _, err := tx.Exec(`INSERT INTO users(username, pwd_hash, group_id) VALUES(?,?,?)`,
		"admin", string(hash), adminGroupID); err != nil {
		return nil, "", err
	}
	if err := tx.Commit(); err != nil {
		return nil, "", err
	}
	return s, adminPass, nil
}

// HashPassword returns a bcrypt hash (cost 10) of p.
func HashPassword(p string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(p), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// CheckPasswordHash reports whether p matches the bcrypt hash.
func CheckPasswordHash(hash, p string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(p)) == nil
}

// Save is a no-op: SQLite commits every statement durably. It is retained
// for API compatibility with callers that flushed the old JSON store.
func (s *Store) Save() error { return nil }

// Users returns all users with their tokens attached.
func (s *Store) Users() []User {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id, username, pwd_hash, group_id FROM users ORDER BY id`)
	if err != nil {
		return nil
	}
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.PwdHash, &u.GroupID); err != nil {
			rows.Close()
			return nil
		}
		out = append(out, u)
	}
	rows.Close()
	// Attach tokens in one extra query (N is small). The rows above are
	// closed first: with a single SQLite connection a live rows set would
	// block the next query.
	idx := make(map[int]int, len(out))
	for i := range out {
		idx[out[i].ID] = i
	}
	trows, err := s.db.Query(`SELECT id, user_id, label, token, agent_id FROM tokens ORDER BY id`)
	if err != nil {
		return out
	}
	defer trows.Close()
	for trows.Next() {
		var t Token
		var uid int
		if err := trows.Scan(&t.ID, &uid, &t.Label, &t.Token, &t.AgentID); err != nil {
			break
		}
		if i, ok := idx[uid]; ok {
			out[i].Tokens = append(out[i].Tokens, t)
		}
	}
	return out
}

// User returns a user (with tokens) by id.
func (s *Store) User(id int) (User, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.userLocked(id)
}

// UserByName returns a user (with tokens) by username.
func (s *Store) UserByName(name string) (User, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.userByNameLocked(name)
}

// CreateUser adds a user and returns it. Usernames must be unique.
func (s *Store) CreateUser(username, password string, groupID int) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(username) == "" {
		return User{}, errors.New("用户名不能为空")
	}
	if !s.hasGroupLocked(groupID) {
		return User{}, fmt.Errorf("权限组 %d 不存在", groupID)
	}
	if s.userExistsLocked(username) {
		return User{}, fmt.Errorf("用户名 %s 已存在", username)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, err
	}
	res, err := s.db.Exec(`INSERT INTO users(username, pwd_hash, group_id) VALUES(?,?,?)`,
		username, string(hash), groupID)
	if err != nil {
		return User{}, err
	}
	id, _ := res.LastInsertId()
	u, _ := s.userLocked(int(id))
	return u, nil
}

// UpdateUser changes a user's group and optionally its password.
func (s *Store) UpdateUser(id, groupID int, newPassword string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.userLocked(id)
	if !ok {
		return fmt.Errorf("用户 %d 不存在", id)
	}
	if !s.hasGroupLocked(groupID) {
		return fmt.Errorf("权限组 %d 不存在", groupID)
	}
	hash := u.PwdHash
	if newPassword != "" {
		h, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		hash = string(h)
	}
	_, err := s.db.Exec(`UPDATE users SET group_id=?, pwd_hash=? WHERE id=?`, groupID, hash, id)
	return err
}

// DeleteUser removes a user, its tokens, and all of its sessions.
func (s *Store) DeleteUser(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.userLocked(id); !ok {
		return fmt.Errorf("用户 %d 不存在", id)
	}
	// Capture the user's token ids before deleting them so their instance
	// cache snapshots can be dropped too.
	var tokenIDs []int
	if rows, err := s.db.Query(`SELECT id FROM tokens WHERE user_id=?`, id); err == nil {
		for rows.Next() {
			var tid int
			if rows.Scan(&tid) == nil {
				tokenIDs = append(tokenIDs, tid)
			}
		}
		rows.Close()
	}
	if _, err := s.db.Exec(`DELETE FROM tokens WHERE user_id=?`, id); err != nil {
		return err
	}
	for _, tid := range tokenIDs {
		if _, err := s.db.Exec(`DELETE FROM instance_cache WHERE token_id=?`, tid); err != nil {
			return err
		}
	}
	if _, err := s.db.Exec(`DELETE FROM users WHERE id=?`, id); err != nil {
		return err
	}
	for k, sess := range s.sessions {
		if sess.userID == id {
			delete(s.sessions, k)
		}
	}
	return nil
}

// CheckPassword verifies a username/password pair.
func (s *Store) CheckPassword(username, password string) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.userByNameLocked(username)
	if !ok {
		return User{}, errors.New("用户名或密码错误")
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PwdHash), []byte(password)) == nil {
		return u, nil
	}
	return User{}, errors.New("用户名或密码错误")
}

// TokensOf returns a user's tokens.
func (s *Store) TokensOf(userID int) []Token {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokensLocked(userID)
}

// AddToken appends a PAT to a user and returns it. New tokens start unbound
// to any agent (AgentID == 0).
func (s *Store) AddToken(userID int, label, token string) (Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.userExistsLockedByID(userID) {
		return Token{}, fmt.Errorf("用户 %d 不存在", userID)
	}
	res, err := s.db.Exec(`INSERT INTO tokens(user_id, label, token, agent_id) VALUES(?,?,?,0)`, userID, label, token)
	if err != nil {
		return Token{}, err
	}
	id, _ := res.LastInsertId()
	return Token{ID: int(id), Label: label, Token: token}, nil
}

// SetTokenAgent binds (agentID > 0) or unbinds (agentID == 0) the agent that
// handles via-agent operations for a user's token (account).
func (s *Store) SetTokenAgent(userID, tokenID, agentID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.userExistsLockedByID(userID) {
		return fmt.Errorf("用户 %d 不存在", userID)
	}
	res, err := s.db.Exec(`UPDATE tokens SET agent_id=? WHERE id=? AND user_id=?`, agentID, tokenID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("令牌 %d 不存在", tokenID)
	}
	return nil
}

// DeleteToken removes a token from a user.
func (s *Store) DeleteToken(userID, tokenID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.userExistsLockedByID(userID) {
		return fmt.Errorf("用户 %d 不存在", userID)
	}
	res, err := s.db.Exec(`DELETE FROM tokens WHERE id=? AND user_id=?`, tokenID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("令牌 %d 不存在", tokenID)
	}
	// Drop the token's cached instance snapshot so it does not linger.
	if _, err := s.db.Exec(`DELETE FROM instance_cache WHERE token_id=?`, tokenID); err != nil {
		return err
	}
	return nil
}

// TokenPAT returns the plaintext PAT for a user's token.
func (s *Store) TokenPAT(userID, tokenID int) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.userExistsLockedByID(userID) {
		return "", fmt.Errorf("用户 %d 不存在", userID)
	}
	var token string
	err := s.db.QueryRow(`SELECT token FROM tokens WHERE id=? AND user_id=?`, tokenID, userID).Scan(&token)
	if err != nil {
		return "", fmt.Errorf("令牌 %d 不存在", tokenID)
	}
	return token, nil
}

// Groups returns all permission groups.
func (s *Store) Groups() []Group {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.groupsLocked()
}

// Group returns a group by id.
func (s *Store) Group(id int) (Group, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.groupLocked(id)
}

// CreateGroup adds a permission group. Names must be unique.
func (s *Store) CreateGroup(name string, perms []string) (Group, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(name) == "" {
		return Group{}, errors.New("组名不能为空")
	}
	if s.groupNameExistsLocked(name) {
		return Group{}, fmt.Errorf("组名 %s 已存在", name)
	}
	if err := validatePerms(perms); err != nil {
		return Group{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Group{}, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO groups(name) VALUES(?)`, name)
	if err != nil {
		return Group{}, err
	}
	gid, _ := res.LastInsertId()
	for _, p := range perms {
		if _, err := tx.Exec(`INSERT INTO group_perms(group_id, perm) VALUES(?,?)`, gid, p); err != nil {
			return Group{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Group{}, err
	}
	return Group{ID: int(gid), Name: name, Perms: append([]string(nil), perms...)}, nil
}

// UpdateGroup renames a group and/or replaces its permissions.
func (s *Store) UpdateGroup(id int, name string, perms []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.groupLocked(id); !ok {
		return fmt.Errorf("权限组 %d 不存在", id)
	}
	if strings.TrimSpace(name) == "" {
		return errors.New("组名不能为空")
	}
	if s.groupNameExistsLocked(name) {
		return fmt.Errorf("组名 %s 已存在", name)
	}
	if err := validatePerms(perms); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE groups SET name=? WHERE id=?`, name, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM group_perms WHERE group_id=?`, id); err != nil {
		return err
	}
	for _, p := range perms {
		if _, err := tx.Exec(`INSERT INTO group_perms(group_id, perm) VALUES(?,?)`, id, p); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteGroup removes a group; fails while any user still references it.
func (s *Store) DeleteGroup(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groupLocked(id)
	if !ok {
		return fmt.Errorf("权限组 %d 不存在", id)
	}
	var ref int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE group_id=?`, id).Scan(&ref); err != nil {
		return err
	}
	if ref > 0 {
		return fmt.Errorf("权限组 %s 仍被用户引用，无法删除", g.Name)
	}
	if _, err := s.db.Exec(`DELETE FROM group_perms WHERE group_id=?`, id); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM groups WHERE id=?`, id)
	return err
}

// NewSession creates a session for a user and returns the cookie value.
func (s *Store) NewSession(userID int, ttl time.Duration) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	cookie := randomHex(32)
	s.sessions[cookie] = session{userID: userID, expiresAt: time.Now().Add(ttl)}
	return cookie
}

// SessionUser resolves a cookie to its user; expired/unknown cookies fail.
func (s *Store) SessionUser(cookieValue string) (User, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[cookieValue]
	if !ok {
		return User{}, false
	}
	if time.Now().After(sess.expiresAt) {
		delete(s.sessions, cookieValue)
		return User{}, false
	}
	if u, ok := s.userLocked(sess.userID); ok {
		return u, true
	}
	delete(s.sessions, cookieValue)
	return User{}, false
}

// DropSession deletes a session cookie.
func (s *Store) DropSession(cookieValue string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, cookieValue)
}

// PermsOf returns a copy of a user's effective permissions from its group.
func (s *Store) PermsOf(userID int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var gid int
	if err := s.db.QueryRow(`SELECT group_id FROM users WHERE id=?`, userID).Scan(&gid); err != nil {
		return nil
	}
	return s.permsLocked(gid)
}

// RegCode returns the persisted agent registration code ("" when disabled).
func (s *Store) RegCode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.metaLocked("reg_code")
}

// SetRegCode persists the agent registration code. An empty code disables
// agent registration.
func (s *Store) SetRegCode(code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setMetaLocked("reg_code", code)
}

// SelfAgentName returns the persisted custom name for the panel's own
// self-agent, or "" when it has not been renamed from the admin UI.
func (s *Store) SelfAgentName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.metaLocked("self_agent_name")
}

// SetSelfAgentName persists the custom name for the panel's own self-agent,
// so the rename survives restarts.
func (s *Store) SetSelfAgentName(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setMetaLocked("self_agent_name", name)
}

// InstanceScript returns the script configured for an instance (ok=false when
// none is configured, i.e. the instance uses the built-in default template).
func (s *Store) InstanceScript(instanceID int) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var script string
	err := s.db.QueryRow(`SELECT script FROM instance_scripts WHERE instance_id=?`, instanceID).Scan(&script)
	if err != nil {
		return "", false
	}
	return script, true
}

// SetInstanceScript persists the script configured for an instance.
func (s *Store) SetInstanceScript(instanceID int, script string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO instance_scripts(instance_id, script) VALUES(?, ?)
		 ON CONFLICT(instance_id) DO UPDATE SET script=excluded.script`, instanceID, script)
	return err
}

// DeleteInstanceScript removes the instance's script configuration, so the
// instance falls back to the built-in default template.
func (s *Store) DeleteInstanceScript(instanceID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM instance_scripts WHERE instance_id=?`, instanceID)
	return err
}

// InstanceScriptIDs returns the ids of instances that have a custom script
// configured.
func (s *Store) InstanceScriptIDs() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT instance_id FROM instance_scripts ORDER BY instance_id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			break
		}
		out = append(out, id)
	}
	return out
}

// CacheInstances replaces the cached instance snapshot for a token with the
// given instances, recording the fetch time. Called after a successful
// ListInstances so a later API failure can fall back to this snapshot.
func (s *Store) CacheInstances(tokenID int, insts []linode.Instance) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fetchedAt := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM instance_cache WHERE token_id=?`, tokenID); err != nil {
		return err
	}
	for _, in := range insts {
		ipv4 := in.IPv4
		if ipv4 == nil {
			ipv4 = []string{}
		}
		tags := in.Tags
		if tags == nil {
			tags = []string{}
		}
		ipv4JSON, err := json.Marshal(ipv4)
		if err != nil {
			return err
		}
		tagsJSON, err := json.Marshal(tags)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT INTO instance_cache
				(token_id, instance_id, label, status, region, type, image,
				 ipv4, ipv6, tags, created, updated, fetched_at)
			 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			tokenID, in.ID, in.Label, in.Status, in.Region, in.Type, in.Image,
			string(ipv4JSON), in.IPv6, string(tagsJSON), in.Created, in.Updated, fetchedAt,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// CachedInstances returns the cached instance snapshot for a token, or nil
// when nothing is cached.
func (s *Store) CachedInstances(tokenID int) []linode.Instance {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(
		`SELECT instance_id, label, status, region, type, image, ipv4, ipv6,
		        tags, created, updated
		   FROM instance_cache WHERE token_id=? ORDER BY instance_id`, tokenID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []linode.Instance
	for rows.Next() {
		var in linode.Instance
		var ipv4, tags string
		if err := rows.Scan(&in.ID, &in.Label, &in.Status, &in.Region, &in.Type,
			&in.Image, &ipv4, &in.IPv6, &tags, &in.Created, &in.Updated); err != nil {
			return out
		}
		_ = json.Unmarshal([]byte(ipv4), &in.IPv4)
		_ = json.Unmarshal([]byte(tags), &in.Tags)
		out = append(out, in)
	}
	return out
}

// InvalidateInstanceCache drops the cached snapshot for a token so the next
// list refetches from the Linode API. Called after mutating operations
// (power actions, create, rebuild) to avoid serving stale cached state.
func (s *Store) InvalidateInstanceCache(tokenID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM instance_cache WHERE token_id=?`, tokenID)
	return err
}

// UpsertInstanceCache inserts or updates a single instance within a token's
// cached snapshot, refreshing its fetched time. Used by the per-row refresh
// endpoint so only the refreshed instance changes in the cache.
func (s *Store) UpsertInstanceCache(tokenID int, inst linode.Instance) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fetchedAt := time.Now().UTC().Format(time.RFC3339Nano)
	ipv4 := inst.IPv4
	if ipv4 == nil {
		ipv4 = []string{}
	}
	tags := inst.Tags
	if tags == nil {
		tags = []string{}
	}
	ipv4JSON, err := json.Marshal(ipv4)
	if err != nil {
		return err
	}
	tagsJSON, err := json.Marshal(tags)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO instance_cache
			(token_id, instance_id, label, status, region, type, image,
			 ipv4, ipv6, tags, created, updated, fetched_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(token_id, instance_id) DO UPDATE SET
			label=excluded.label, status=excluded.status, region=excluded.region,
			type=excluded.type, image=excluded.image, ipv4=excluded.ipv4,
			ipv6=excluded.ipv6, tags=excluded.tags, created=excluded.created,
			updated=excluded.updated, fetched_at=excluded.fetched_at`,
		tokenID, inst.ID, inst.Label, inst.Status, inst.Region, inst.Type, inst.Image,
		string(ipv4JSON), inst.IPv6, string(tagsJSON), inst.Created, inst.Updated, fetchedAt,
	)
	return err
}

// --- internal helpers (callers hold s.mu) ---

func (s *Store) userLocked(id int) (User, bool) {
	row := s.db.QueryRow(`SELECT id, username, pwd_hash, group_id FROM users WHERE id=?`, id)
	var u User
	if err := row.Scan(&u.ID, &u.Username, &u.PwdHash, &u.GroupID); err != nil {
		return User{}, false
	}
	u.Tokens = s.tokensLocked(u.ID)
	return u, true
}

func (s *Store) userByNameLocked(name string) (User, bool) {
	row := s.db.QueryRow(`SELECT id, username, pwd_hash, group_id FROM users WHERE username=?`, name)
	var u User
	if err := row.Scan(&u.ID, &u.Username, &u.PwdHash, &u.GroupID); err != nil {
		return User{}, false
	}
	u.Tokens = s.tokensLocked(u.ID)
	return u, true
}

func (s *Store) tokensLocked(userID int) []Token {
	rows, err := s.db.Query(`SELECT id, label, token, agent_id FROM tokens WHERE user_id=? ORDER BY id`, userID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Token
	for rows.Next() {
		var t Token
		if err := rows.Scan(&t.ID, &t.Label, &t.Token, &t.AgentID); err != nil {
			break
		}
		out = append(out, t)
	}
	return out
}

func (s *Store) userExistsLocked(username string) bool {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE username=?`, username).Scan(&n)
	return n > 0
}

func (s *Store) userExistsLockedByID(id int) bool {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE id=?`, id).Scan(&n)
	return n > 0
}

func (s *Store) hasGroupLocked(id int) bool {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM groups WHERE id=?`, id).Scan(&n)
	return n > 0
}

func (s *Store) groupNameExistsLocked(name string) bool {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM groups WHERE name=?`, name).Scan(&n)
	return n > 0
}

func (s *Store) groupLocked(id int) (Group, bool) {
	row := s.db.QueryRow(`SELECT id, name FROM groups WHERE id=?`, id)
	var g Group
	if err := row.Scan(&g.ID, &g.Name); err != nil {
		return Group{}, false
	}
	g.Perms = s.permsLocked(g.ID)
	return g, true
}

func (s *Store) groupsLocked() []Group {
	rows, err := s.db.Query(`SELECT id, name FROM groups ORDER BY id`)
	if err != nil {
		return nil
	}
	var out []Group
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name); err != nil {
			rows.Close()
			return nil
		}
		out = append(out, g)
	}
	rows.Close()
	// Load perms after closing rows: a single SQLite connection cannot serve
	// a nested query while rows are still open.
	for i := range out {
		out[i].Perms = s.permsLocked(out[i].ID)
	}
	return out
}

func (s *Store) permsLocked(groupID int) []string {
	rows, err := s.db.Query(`SELECT perm FROM group_perms WHERE group_id=? ORDER BY rowid`, groupID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			break
		}
		out = append(out, p)
	}
	return out
}

func (s *Store) metaLocked(key string) string {
	var v string
	if err := s.db.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&v); err != nil {
		return ""
	}
	return v
}

func (s *Store) setMetaLocked(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO meta(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func validatePerms(perms []string) error {
	valid := make(map[string]bool, len(ValidPerms()))
	for _, p := range ValidPerms() {
		valid[p] = true
	}
	for _, p := range perms {
		if !valid[p] {
			return fmt.Errorf("未知权限 %q", p)
		}
	}
	return nil
}

// randomHex returns n random bytes as a hex string.
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand.Read never fails on supported platforms; panic is safest.
		panic(err)
	}
	return hex.EncodeToString(b)
}
