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
	key      []byte
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
//
// Secrets (PATs, root passwords, the agent registration code) are encrypted
// at rest. The master key comes from LINODE_PANEL_SECRET_KEY when set,
// otherwise from a key file next to the database (created on first use).
func Open(path string) (*Store, string, error) {
	return OpenWithKey(path, "")
}

// OpenWithKey is Open with an explicit master key. A non-empty key takes
// precedence over LINODE_PANEL_SECRET_KEY and the auto-generated key file.
func OpenWithKey(path, secretKey string) (*Store, string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, "", fmt.Errorf("创建数据库目录: %w", err)
	}
	key, err := resolveSecretKey(path, secretKey)
	if err != nil {
		return nil, "", err
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
	s := &Store{db: db, key: key, sessions: make(map[string]session)}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, "", err
	}
	// Encrypt secrets written in plaintext by older versions. Failure here
	// must abort startup: continuing would silently write plaintext.
	if err := s.encryptLegacySecrets(); err != nil {
		db.Close()
		return nil, "", fmt.Errorf("加密数据库中的敏感字段: %w", err)
	}
	if err := s.verifySecretsDecryptable(); err != nil {
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
	cache_id INTEGER PRIMARY KEY,
	script   TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS instance_root_passwords (
	cache_id INTEGER PRIMARY KEY,
	password TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS instance_auto_rebuild (
	cache_id        INTEGER PRIMARY KEY,
	user_id         INTEGER NOT NULL DEFAULT 0,
	token_id        INTEGER NOT NULL DEFAULT 0,
	enabled         INTEGER NOT NULL DEFAULT 0,
	port            INTEGER NOT NULL DEFAULT 22,
	threshold       INTEGER NOT NULL DEFAULT 3,
	interval_sec    INTEGER NOT NULL DEFAULT 300,
	fail_count      INTEGER NOT NULL DEFAULT 0,
	last_status     TEXT NOT NULL DEFAULT '',
	last_detail     TEXT NOT NULL DEFAULT '',
	last_check_at   TEXT NOT NULL DEFAULT '',
	last_blocked_at TEXT NOT NULL DEFAULT '',
	last_task_id    TEXT NOT NULL DEFAULT '',
	updated_at      TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS instance_cache (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	token_id    INTEGER NOT NULL DEFAULT 0,
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
	alive       INTEGER NOT NULL DEFAULT 1,
	stale       INTEGER NOT NULL DEFAULT 0,
	UNIQUE(token_id, instance_id)
);
CREATE TABLE IF NOT EXISTS account_groups (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id        INTEGER NOT NULL,
	name           TEXT NOT NULL,
	created_at     TEXT NOT NULL DEFAULT '',
	instance_config TEXT NOT NULL DEFAULT '',
	UNIQUE(user_id, name)
);
CREATE TABLE IF NOT EXISTS account_group_members (
	group_id INTEGER NOT NULL,
	token_id INTEGER NOT NULL,
	PRIMARY KEY (group_id, token_id)
);
CREATE TABLE IF NOT EXISTS instance_failover (
	cache_id     INTEGER PRIMARY KEY,
	user_id      INTEGER NOT NULL DEFAULT 0,
	group_id     INTEGER NOT NULL DEFAULT 0,
	token_id     INTEGER NOT NULL DEFAULT 0,
	enabled      INTEGER NOT NULL DEFAULT 0,
	threshold    INTEGER NOT NULL DEFAULT 3,
	interval_sec INTEGER NOT NULL DEFAULT 300,
	fail_count   INTEGER NOT NULL DEFAULT 0,
	last_status  TEXT NOT NULL DEFAULT '',
	last_detail  TEXT NOT NULL DEFAULT '',
	last_check_at TEXT NOT NULL DEFAULT '',
	last_task_id TEXT NOT NULL DEFAULT '',
	replaced_by  INTEGER NOT NULL DEFAULT 0,
	updated_at   TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS account_options_cache (
	token_id   INTEGER PRIMARY KEY,
	regions    TEXT NOT NULL,
	types      TEXT NOT NULL,
	images     TEXT NOT NULL,
	fetched_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS account_dual (
	token_id            INTEGER PRIMARY KEY,
	user_id             INTEGER NOT NULL DEFAULT 0,
	enabled             INTEGER NOT NULL DEFAULT 0,
	port                INTEGER NOT NULL DEFAULT 22,
	threshold           INTEGER NOT NULL DEFAULT 3,
	interval_sec        INTEGER NOT NULL DEFAULT 300,
	active_instance_id  INTEGER NOT NULL DEFAULT 0,
	standby_instance_id INTEGER NOT NULL DEFAULT 0,
	state               TEXT NOT NULL DEFAULT 'idle',
	fail_count          INTEGER NOT NULL DEFAULT 0,
	last_status         TEXT NOT NULL DEFAULT '',
	last_detail         TEXT NOT NULL DEFAULT '',
	last_check_at       TEXT NOT NULL DEFAULT '',
	last_blocked_at     TEXT NOT NULL DEFAULT '',
	script              TEXT NOT NULL DEFAULT '',
	root_password       TEXT NOT NULL DEFAULT '',
	updated_at          TEXT NOT NULL DEFAULT ''
);`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("初始化表结构: %w", err)
	}
	// 旧库的 tokens 表缺少 agent_id 列（账号绑定 agent 功能），需要补列。
	if err := s.ensureTokenAgentColumn(); err != nil {
		return fmt.Errorf("迁移 tokens 表: %w", err)
	}
	// 旧库的 account_groups 表缺少 instance_config 列（目标开机配置），需要补列。
	if err := s.ensureAccountGroupInstanceConfigColumn(); err != nil {
		return fmt.Errorf("迁移 account_groups 表: %w", err)
	}
	// instance_cache 需要自增 id + alive/stale（稳定实例标识），旧库需要重建表。
	if err := s.ensureInstanceCacheIDColumn(); err != nil {
		return fmt.Errorf("迁移 instance_cache 表: %w", err)
	}
	// 配置表原先按 Linode 实例 id 关联；改为按 instance_cache.id（稳定标识）
	// 关联后，删机重建只需更新缓存行的 instance_id，配置自动跟随新实例。
	if err := s.ensureInstanceConfigCacheIDs(); err != nil {
		return fmt.Errorf("迁移实例配置表: %w", err)
	}
	// 双机表补 last_blocked_at 列（记录每次被墙时间，用于通知间隔）。
	if err := s.ensureDualBlockedAtColumn(); err != nil {
		return fmt.Errorf("迁移 account_dual 表: %w", err)
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

// ensureAccountGroupInstanceConfigColumn adds the instance_config column to an
// existing account_groups table created before the pre-filled boot-config
// feature. New databases get the column from the schema above; this is a no-op
// guard for old ones.
func (s *Store) ensureAccountGroupInstanceConfigColumn() error {
	has, err := s.columnExists("account_groups", "instance_config")
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	_, err = s.db.Exec(`ALTER TABLE account_groups ADD COLUMN instance_config TEXT NOT NULL DEFAULT ''`)
	return err
}

// ensureDualBlockedAtColumn adds last_blocked_at to account_dual for old DBs.
func (s *Store) ensureDualBlockedAtColumn() error {
	has, err := s.columnExists("account_dual", "last_blocked_at")
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	_, err = s.db.Exec(`ALTER TABLE account_dual ADD COLUMN last_blocked_at TEXT NOT NULL DEFAULT ''`)
	return err
}

// instanceCacheDDL is the current shape of instance_cache. The auto-increment
// id is the panel's stable identity for an instance: it survives a rebuild
// (only instance_id changes), so per-instance configuration keyed by cache_id
// is inherited automatically by the replacement instance.
const instanceCacheDDL = `CREATE TABLE %s (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	token_id    INTEGER NOT NULL DEFAULT 0,
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
	alive       INTEGER NOT NULL DEFAULT 1,
	stale       INTEGER NOT NULL DEFAULT 0,
	UNIQUE(token_id, instance_id)
)`

// ensureInstanceCacheIDColumn upgrades an old instance_cache (primary key
// token_id+instance_id) to the current shape with a stable auto-increment id.
func (s *Store) ensureInstanceCacheIDColumn() error {
	has, err := s.columnExists("instance_cache", "id")
	if err != nil {
		return err
	}
	if !has {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		for _, stmt := range []string{
			`DROP TABLE IF EXISTS instance_cache_v1`,
			`ALTER TABLE instance_cache RENAME TO instance_cache_v1`,
			fmt.Sprintf(instanceCacheDDL, "instance_cache"),
			`INSERT INTO instance_cache
			   (token_id, instance_id, label, status, region, type, image,
			    ipv4, ipv6, tags, created, updated, fetched_at, alive, stale)
			 SELECT token_id, instance_id, label, status, region, type, image,
			        ipv4, ipv6, tags, created, updated, fetched_at, 1, 0
			   FROM instance_cache_v1`,
			`DROP TABLE instance_cache_v1`,
		} {
			if _, err := tx.Exec(stmt); err != nil {
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	// Partial/older migrations may already have id but not the state columns.
	for _, col := range []struct {
		name string
		ddl  string
	}{
		{"alive", `ALTER TABLE instance_cache ADD COLUMN alive INTEGER NOT NULL DEFAULT 1`},
		{"stale", `ALTER TABLE instance_cache ADD COLUMN stale INTEGER NOT NULL DEFAULT 0`},
	} {
		has, err := s.columnExists("instance_cache", col.name)
		if err != nil {
			return err
		}
		if !has {
			if _, err := s.db.Exec(col.ddl); err != nil {
				return err
			}
		}
	}
	return nil
}

// instanceConfigTables describes the per-instance configuration tables and how
// to recreate them keyed by instance_cache.id.
var instanceConfigTables = []struct {
	name string
	// ddl has one %s for the table name and uses cache_id as the primary key.
	ddl string
	// cols are the non-key columns copied from the old table, in order.
	cols []string
}{
	{
		name: "instance_scripts",
		ddl: `CREATE TABLE %s (
			cache_id INTEGER PRIMARY KEY,
			script   TEXT NOT NULL
		)`,
		cols: []string{"script"},
	},
	{
		name: "instance_auto_rebuild",
		ddl: `CREATE TABLE %s (
			cache_id        INTEGER PRIMARY KEY,
			user_id         INTEGER NOT NULL DEFAULT 0,
			token_id        INTEGER NOT NULL DEFAULT 0,
			enabled         INTEGER NOT NULL DEFAULT 0,
			port            INTEGER NOT NULL DEFAULT 22,
			threshold       INTEGER NOT NULL DEFAULT 3,
			interval_sec    INTEGER NOT NULL DEFAULT 300,
			fail_count      INTEGER NOT NULL DEFAULT 0,
			last_status     TEXT NOT NULL DEFAULT '',
			last_detail     TEXT NOT NULL DEFAULT '',
			last_check_at   TEXT NOT NULL DEFAULT '',
			last_blocked_at TEXT NOT NULL DEFAULT '',
			last_task_id    TEXT NOT NULL DEFAULT '',
			updated_at      TEXT NOT NULL DEFAULT ''
		)`,
		cols: []string{"user_id", "token_id", "enabled", "port", "threshold",
			"interval_sec", "fail_count", "last_status", "last_detail",
			"last_check_at", "last_blocked_at", "last_task_id", "updated_at"},
	},
	{
		name: "instance_failover",
		ddl: `CREATE TABLE %s (
			cache_id     INTEGER PRIMARY KEY,
			user_id      INTEGER NOT NULL DEFAULT 0,
			group_id     INTEGER NOT NULL DEFAULT 0,
			token_id     INTEGER NOT NULL DEFAULT 0,
			enabled      INTEGER NOT NULL DEFAULT 0,
			threshold    INTEGER NOT NULL DEFAULT 3,
			interval_sec INTEGER NOT NULL DEFAULT 300,
			fail_count   INTEGER NOT NULL DEFAULT 0,
			last_status  TEXT NOT NULL DEFAULT '',
			last_detail  TEXT NOT NULL DEFAULT '',
			last_check_at TEXT NOT NULL DEFAULT '',
			last_task_id TEXT NOT NULL DEFAULT '',
			replaced_by  INTEGER NOT NULL DEFAULT 0,
			updated_at   TEXT NOT NULL DEFAULT ''
		)`,
		cols: []string{"user_id", "group_id", "token_id", "enabled", "threshold",
			"interval_sec", "fail_count", "last_status", "last_detail",
			"last_check_at", "last_task_id", "replaced_by", "updated_at"},
	},
}

// ensureInstanceConfigCacheIDs upgrades config tables that were keyed by the
// volatile Linode instance id to key by the stable instance_cache.id.
func (s *Store) ensureInstanceConfigCacheIDs() error {
	for _, t := range instanceConfigTables {
		has, err := s.columnExists(t.name, "cache_id")
		if err != nil {
			return err
		}
		if has {
			continue
		}
		hasOld, err := s.columnExists(t.name, "instance_id")
		if err != nil {
			return err
		}
		if !hasOld {
			continue
		}
		if err := s.migrateConfigTableToCacheID(t.name, t.ddl, t.cols); err != nil {
			return err
		}
	}
	return nil
}

// migrateConfigTableToCacheID rebuilds one config table with cache_id keys.
// Each old instance_id is mapped to the canonical instance_cache.id (the
// smallest id for that Linode instance); a token-less anchor row is created
// when the instance was never cached.
func (s *Store) migrateConfigTableToCacheID(name, ddl string, cols []string) error {
	tmp := name + "_cacheid_new"
	old := name + "_instanceid_old"
	colList := strings.Join(cols, ", ")
	oldList := "o." + strings.Join(cols, ", o.")
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS ` + tmp,
		`DROP TABLE IF EXISTS ` + old,
		`ALTER TABLE ` + name + ` RENAME TO ` + old,
		fmt.Sprintf(ddl, tmp),
		`INSERT INTO instance_cache (token_id, instance_id, fetched_at)
		   SELECT 0, o.instance_id, ''
		     FROM ` + old + ` o
		    WHERE NOT EXISTS (
		          SELECT 1 FROM instance_cache c WHERE c.instance_id = o.instance_id)`,
		`INSERT INTO ` + tmp + ` (cache_id, ` + colList + `)
		   SELECT (SELECT MIN(c.id) FROM instance_cache c WHERE c.instance_id = o.instance_id),
		          ` + oldList + `
		     FROM ` + old + ` o`,
		`DROP TABLE ` + old,
		`ALTER TABLE ` + tmp + ` RENAME TO ` + name,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// cacheIDForInstanceLocked returns the canonical cache id for a Linode
// instance id (the smallest id across all tokens' snapshots). When ensure is
// true and no row exists, a token-less anchor row is inserted so configuration
// can be attached before the instance has ever been listed.
func (s *Store) cacheIDForInstanceLocked(instanceID int, ensure bool) (int, bool, error) {
	var id int
	err := s.db.QueryRow(
		`SELECT id FROM instance_cache WHERE instance_id=? ORDER BY id LIMIT 1`, instanceID).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	if !ensure {
		return 0, false, nil
	}
	res, err := s.db.Exec(
		`INSERT INTO instance_cache (token_id, instance_id, fetched_at) VALUES(0, ?, '')`, instanceID)
	if err != nil {
		return 0, false, err
	}
	last, err := res.LastInsertId()
	if err != nil {
		return 0, false, err
	}
	return int(last), true, nil
}

// dropTokenCacheLocked hides a token's cached rows. Rows still referenced by
// per-instance configuration are kept as dead anchors (so the stable id keeps
// resolving); the rest are deleted.
func (s *Store) dropTokenCacheLocked(tokenID int) error {
	const referenced = `id IN (SELECT cache_id FROM instance_scripts
	                           UNION SELECT cache_id FROM instance_root_passwords
	                           UNION SELECT cache_id FROM instance_auto_rebuild
	                           UNION SELECT cache_id FROM instance_failover)`
	if _, err := s.db.Exec(
		`UPDATE instance_cache SET alive=0, stale=1 WHERE token_id=? AND `+referenced, tokenID); err != nil {
		return err
	}
	_, err := s.db.Exec(
		`DELETE FROM instance_cache WHERE token_id=? AND NOT (`+referenced+`)`, tokenID)
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
		if plain, err := s.decryptSecret(t.Token); err == nil {
			t.Token = plain
		} else {
			t.Token = ""
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
		if err := s.dropTokenCacheLocked(tid); err != nil {
			return err
		}
		if _, err := s.db.Exec(`DELETE FROM account_options_cache WHERE token_id=?`, tid); err != nil {
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
	enc, err := s.encryptSecret(token)
	if err != nil {
		return Token{}, err
	}
	res, err := s.db.Exec(`INSERT INTO tokens(user_id, label, token, agent_id) VALUES(?,?,?,0)`, userID, label, enc)
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

// UpdateTokenLabel renames a user's token (account).
func (s *Store) UpdateTokenLabel(userID, tokenID int, newLabel string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.userExistsLockedByID(userID) {
		return fmt.Errorf("用户 %d 不存在", userID)
	}
	res, err := s.db.Exec(`UPDATE tokens SET label=? WHERE id=? AND user_id=?`, newLabel, tokenID, userID)
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
	if err := s.dropTokenCacheLocked(tokenID); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM account_options_cache WHERE token_id=?`, tokenID); err != nil {
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
	plain, err := s.decryptSecret(token)
	if err != nil {
		return "", fmt.Errorf("令牌 %d: %w", tokenID, err)
	}
	return plain, nil
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
	return s.metaSecretLocked("reg_code")
}

// SetRegCode persists the agent registration code. An empty code disables
// agent registration.
func (s *Store) SetRegCode(code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	enc, err := s.encryptSecret(code)
	if err != nil {
		return err
	}
	return s.setMetaLocked("reg_code", enc)
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
// Configuration is stored against the instance's stable cache id, so it
// survives a delete+recreate rebuild automatically.
func (s *Store) InstanceScript(instanceID int) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cacheID, ok, err := s.cacheIDForInstanceLocked(instanceID, false)
	if err != nil || !ok {
		return "", false
	}
	return s.instanceScriptLocked(cacheID)
}

// InstanceScriptForCacheID is InstanceScript addressed by the stable cache id,
// avoiding ambiguity when several tokens' snapshots reference the same Linode
// instance id.
func (s *Store) InstanceScriptForCacheID(cacheID int) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.instanceScriptLocked(cacheID)
}

func (s *Store) instanceScriptLocked(cacheID int) (string, bool) {
	var script string
	err := s.db.QueryRow(`SELECT script FROM instance_scripts WHERE cache_id=?`, cacheID).Scan(&script)
	if err != nil {
		return "", false
	}
	return script, true
}

// SetInstanceScript persists the script configured for an instance.
func (s *Store) SetInstanceScript(instanceID int, script string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cacheID, _, err := s.cacheIDForInstanceLocked(instanceID, true)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO instance_scripts(cache_id, script) VALUES(?, ?)
		 ON CONFLICT(cache_id) DO UPDATE SET script=excluded.script`, cacheID, script)
	return err
}

// DeleteInstanceScript removes the instance's script configuration, so the
// instance falls back to the built-in default template.
func (s *Store) DeleteInstanceScript(instanceID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cacheID, ok, err := s.cacheIDForInstanceLocked(instanceID, false)
	if err != nil || !ok {
		return err
	}
	_, err = s.db.Exec(`DELETE FROM instance_scripts WHERE cache_id=?`, cacheID)
	return err
}

// InstanceRootPassword returns the root password saved for an instance. The
// password is stored against the instance's stable cache id, so a rebuild
// inherits it automatically.
func (s *Store) InstanceRootPassword(instanceID int) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cacheID, ok, err := s.cacheIDForInstanceLocked(instanceID, false)
	if err != nil || !ok {
		return "", false
	}
	return s.instanceRootPasswordLocked(cacheID)
}

// InstanceRootPasswordForCacheID is InstanceRootPassword addressed by the
// stable cache id (unambiguous across tokens' snapshots).
func (s *Store) InstanceRootPasswordForCacheID(cacheID int) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.instanceRootPasswordLocked(cacheID)
}

func (s *Store) instanceRootPasswordLocked(cacheID int) (string, bool) {
	var password string
	err := s.db.QueryRow(
		`SELECT password FROM instance_root_passwords WHERE cache_id=?`, cacheID).Scan(&password)
	if err != nil {
		return "", false
	}
	plain, err := s.decryptSecret(password)
	if err != nil {
		return "", false
	}
	return plain, true
}

// InstanceRootPasswordConfigured reports whether a root password is saved for
// an instance, without exposing the secret.
func (s *Store) InstanceRootPasswordConfigured(instanceID int) bool {
	_, ok := s.InstanceRootPassword(instanceID)
	return ok
}

// SetInstanceRootPassword persists the root password used when (re)creating an
// instance.
func (s *Store) SetInstanceRootPassword(instanceID int, password string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cacheID, _, err := s.cacheIDForInstanceLocked(instanceID, true)
	if err != nil {
		return err
	}
	enc, err := s.encryptSecret(password)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO instance_root_passwords(cache_id, password) VALUES(?, ?)
		 ON CONFLICT(cache_id) DO UPDATE SET password=excluded.password`, cacheID, enc)
	return err
}

// DeleteInstanceRootPassword removes the saved root password.
func (s *Store) DeleteInstanceRootPassword(instanceID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cacheID, ok, err := s.cacheIDForInstanceLocked(instanceID, false)
	if err != nil || !ok {
		return err
	}
	_, err = s.db.Exec(`DELETE FROM instance_root_passwords WHERE cache_id=?`, cacheID)
	return err
}

// InstanceScriptIDs returns the current Linode instance ids that have a custom
// script configured.
func (s *Store) InstanceScriptIDs() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(
		`SELECT DISTINCT c.instance_id
		   FROM instance_scripts s JOIN instance_cache c ON c.id = s.cache_id
		  ORDER BY c.instance_id`)
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
// given instances, recording the fetch time. Rows are upserted (not deleted
// and re-inserted) so each instance keeps its stable auto-increment id across
// refreshes; rows that disappeared from the snapshot are marked dead (alive=0)
// but always kept. The list still shows them as unavailable and the user can
// migrate them to another account, so dropping them would lose the record.
func (s *Store) CacheInstances(tokenID int, insts []linode.Instance) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fetchedAt := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	seen := make([]int, 0, len(insts))
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
				 ipv4, ipv6, tags, created, updated, fetched_at, alive, stale)
			 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,1,0)
			 ON CONFLICT(token_id, instance_id) DO UPDATE SET
				label=excluded.label, status=excluded.status, region=excluded.region,
				type=excluded.type, image=excluded.image, ipv4=excluded.ipv4,
				ipv6=excluded.ipv6, tags=excluded.tags, created=excluded.created,
				updated=excluded.updated, fetched_at=excluded.fetched_at,
				alive=1, stale=0`,
			tokenID, in.ID, in.Label, in.Status, in.Region, in.Type, in.Image,
			string(ipv4JSON), in.IPv6, string(tagsJSON), in.Created, in.Updated, fetchedAt,
		); err != nil {
			return err
		}
		seen = append(seen, in.ID)
	}
	// Mark rows missing from this snapshot as dead instead of deleting them:
	// a stable id must survive both a rebuild (instance temporarily absent)
	// and configuration that still points at it. Missing rows stay in the
	// snapshot so the UI can list them as unavailable and offer migration.
	if len(seen) == 0 {
		if _, err := tx.Exec(`UPDATE instance_cache SET alive=0 WHERE token_id=?`, tokenID); err != nil {
			return err
		}
	} else {
		args := make([]any, 0, len(seen)+1)
		args = append(args, tokenID)
		for _, id := range seen {
			args = append(args, id)
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(seen)), ",")
		if _, err := tx.Exec(
			`UPDATE instance_cache SET alive=0
			  WHERE token_id=? AND instance_id NOT IN (`+placeholders+`)`, args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// CachedInstanceRow is one row of a token's instance snapshot: the last known
// Linode instance plus the panel's stable cache id and whether the instance
// still existed upstream when the snapshot was last refreshed. Alive=false
// means the upstream instance is gone (shown as unavailable, kept for manual
// migration).
type CachedInstanceRow struct {
	CacheID  int
	Instance linode.Instance
	Alive    bool
}

// CachedInstanceRows returns every row of a token's snapshot (alive first),
// including missing instances kept for display and manual migration.
func (s *Store) CachedInstanceRows(tokenID int) []CachedInstanceRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(
		`SELECT id, instance_id, label, status, region, type, image, ipv4, ipv6,
		        tags, created, updated, alive
		   FROM instance_cache WHERE token_id=?
		  ORDER BY alive DESC, instance_id`, tokenID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []CachedInstanceRow
	for rows.Next() {
		var r CachedInstanceRow
		var ipv4, tags string
		var alive int
		if err := rows.Scan(&r.CacheID, &r.Instance.ID, &r.Instance.Label,
			&r.Instance.Status, &r.Instance.Region, &r.Instance.Type,
			&r.Instance.Image, &ipv4, &r.Instance.IPv6, &tags,
			&r.Instance.Created, &r.Instance.Updated, &alive); err != nil {
			return out
		}
		r.Alive = alive != 0
		_ = json.Unmarshal([]byte(ipv4), &r.Instance.IPv4)
		_ = json.Unmarshal([]byte(tags), &r.Instance.Tags)
		out = append(out, r)
	}
	return out
}

// InstanceCacheRow returns one token's cached row for a Linode instance id.
func (s *Store) InstanceCacheRow(tokenID, instanceID int) (CachedInstanceRow, bool) {
	if tokenID <= 0 || instanceID <= 0 {
		return CachedInstanceRow{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var r CachedInstanceRow
	var ipv4, tags string
	var alive int
	err := s.db.QueryRow(
		`SELECT id, instance_id, label, status, region, type, image, ipv4, ipv6,
		        tags, created, updated, alive
		   FROM instance_cache WHERE token_id=? AND instance_id=?`,
		tokenID, instanceID).Scan(
		&r.CacheID, &r.Instance.ID, &r.Instance.Label, &r.Instance.Status,
		&r.Instance.Region, &r.Instance.Type, &r.Instance.Image, &ipv4,
		&r.Instance.IPv6, &tags, &r.Instance.Created, &r.Instance.Updated, &alive)
	if err != nil {
		return CachedInstanceRow{}, false
	}
	r.Alive = alive != 0
	_ = json.Unmarshal([]byte(ipv4), &r.Instance.IPv4)
	_ = json.Unmarshal([]byte(tags), &r.Instance.Tags)
	return r, true
}

// CachedInstances returns the cached instance snapshot for a token, or nil
// when nothing is cached. Missing instances (alive=0) are excluded; use
// CachedInstanceRows when they should be shown as unavailable.
func (s *Store) CachedInstances(tokenID int) []linode.Instance {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(
		`SELECT instance_id, label, status, region, type, image, ipv4, ipv6,
		        tags, created, updated
		   FROM instance_cache WHERE token_id=? AND alive=1 ORDER BY instance_id`, tokenID)
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

// CachedInstance returns the panel's cached snapshot of one Linode instance
// (identified by its Linode id), preferring a live row over a dead anchor. It
// is the fallback used when the instance no longer exists upstream (404): the
// panel can still recreate it from the last known region/type/image.
func (s *Store) CachedInstance(instanceID int) (linode.Instance, bool) {
	if instanceID <= 0 {
		return linode.Instance{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var in linode.Instance
	var ipv4, tags string
	err := s.db.QueryRow(
		`SELECT instance_id, label, status, region, type, image, ipv4, ipv6,
		        tags, created, updated
		   FROM instance_cache WHERE instance_id=?
		  ORDER BY alive DESC, id LIMIT 1`, instanceID).Scan(
		&in.ID, &in.Label, &in.Status, &in.Region, &in.Type, &in.Image,
		&ipv4, &in.IPv6, &tags, &in.Created, &in.Updated)
	if err != nil {
		return linode.Instance{}, false
	}
	_ = json.Unmarshal([]byte(ipv4), &in.IPv4)
	_ = json.Unmarshal([]byte(tags), &in.Tags)
	return in, true
}

// InstanceCacheStale reports whether a token's snapshot was marked stale by a
// mutating action, so the list should refetch from the Linode API.
func (s *Store) InstanceCacheStale(tokenID int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM instance_cache WHERE token_id=? AND alive=1 AND stale=1`, tokenID).Scan(&n)
	return err == nil && n > 0
}

// InvalidateInstanceCache marks a token's cached snapshot stale so the next
// list refetches from the Linode API. Called after mutating operations (power
// actions, create, rebuild). Rows (and their stable ids) are kept, so
// per-instance configuration is not lost.
func (s *Store) InvalidateInstanceCache(tokenID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE instance_cache SET stale=1 WHERE token_id=?`, tokenID)
	return err
}

// MarkInstanceMissing marks one instance as no longer existing upstream
// (alive=0). The row and its configuration are kept: the list shows it as
// unavailable and the user can migrate it to another account. Without this,
// the cache-first list would keep showing the row as healthy.
func (s *Store) MarkInstanceMissing(tokenID, instanceID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`UPDATE instance_cache SET alive=0 WHERE token_id=? AND instance_id=?`, tokenID, instanceID)
	return err
}

// DeleteInstanceCache permanently removes one instance's cache row together
// with its panel-side configuration. Used for an explicit user delete (or to
// purge the record of an instance that is already gone upstream).
func (s *Store) DeleteInstanceCache(tokenID, instanceID int) error {
	if tokenID <= 0 || instanceID <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var cacheID int
	err = tx.QueryRow(
		`SELECT id FROM instance_cache WHERE token_id=? AND instance_id=?`,
		tokenID, instanceID).Scan(&cacheID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, table := range []string{
		"instance_scripts", "instance_root_passwords",
		"instance_auto_rebuild", "instance_failover",
	} {
		if _, err := tx.Exec(`DELETE FROM `+table+` WHERE cache_id=?`, cacheID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM instance_cache WHERE id=?`, cacheID); err != nil {
		return err
	}
	return tx.Commit()
}

// MigrateInstanceCache moves an instance's stable cache row to another account
// and points it at the instance created there. The cache id is unchanged, so
// per-instance configuration (script, root password, auto-rebuild, failover)
// follows automatically; the config rows' bound token is updated so background
// monitors probe the new account.
func (s *Store) MigrateInstanceCache(cacheID, targetTokenID int, inst linode.Instance) error {
	if cacheID <= 0 || targetTokenID <= 0 || inst.ID <= 0 {
		return fmt.Errorf("迁移实例缓存: 参数无效")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// A row may already exist for the new instance under the target account
	// (e.g. the account was refreshed mid-migration). Fold its configuration
	// into the migrating row, letting the migrated row's config win, then drop
	// the duplicate.
	var dupID int
	err = tx.QueryRow(
		`SELECT id FROM instance_cache WHERE token_id=? AND instance_id=?`,
		targetTokenID, inst.ID).Scan(&dupID)
	switch {
	case err == nil && dupID != cacheID:
		if err := mergeConfigToCacheID(tx, dupID, cacheID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM instance_cache WHERE id=?`, dupID); err != nil {
			return err
		}
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return err
	}

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
	fetchedAt := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.Exec(
		`UPDATE instance_cache
		    SET token_id=?, instance_id=?, label=?, status=?, region=?, type=?,
		        image=?, ipv4=?, ipv6=?, tags=?, created=?, updated=?,
		        fetched_at=?, alive=1, stale=0
		  WHERE id=?`,
		targetTokenID, inst.ID, inst.Label, inst.Status, inst.Region, inst.Type,
		inst.Image, string(ipv4JSON), inst.IPv6, string(tagsJSON),
		inst.Created, inst.Updated, fetchedAt, cacheID); err != nil {
		return err
	}
	// Background monitors read the account from the config row; point them at
	// the account now hosting the instance.
	if _, err := tx.Exec(
		`UPDATE instance_auto_rebuild SET token_id=? WHERE cache_id=?`,
		targetTokenID, cacheID); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE instance_failover SET token_id=? WHERE cache_id=?`,
		targetTokenID, cacheID); err != nil {
		return err
	}
	return tx.Commit()
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
			 ipv4, ipv6, tags, created, updated, fetched_at, alive, stale)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,1,0)
		 ON CONFLICT(token_id, instance_id) DO UPDATE SET
			label=excluded.label, status=excluded.status, region=excluded.region,
			type=excluded.type, image=excluded.image, ipv4=excluded.ipv4,
			ipv6=excluded.ipv6, tags=excluded.tags, created=excluded.created,
			updated=excluded.updated, fetched_at=excluded.fetched_at,
			alive=1, stale=0`,
		tokenID, inst.ID, inst.Label, inst.Status, inst.Region, inst.Type, inst.Image,
		string(ipv4JSON), inst.IPv6, string(tagsJSON), inst.Created, inst.Updated, fetchedAt,
	)
	return err
}

// RepointInstanceCache records that the Linode instance oldID was replaced by
// newID (delete+recreate rebuild). Every cached row for oldID is updated in
// place, keeping its stable id, so per-instance configuration keyed by that id
// is inherited by the new instance without any copying.
func (s *Store) RepointInstanceCache(oldID, newID int) error {
	if oldID <= 0 || newID <= 0 || oldID == newID {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	type cacheRow struct{ id, tokenID int }
	var rows []cacheRow
	rs, err := tx.Query(`SELECT id, token_id FROM instance_cache WHERE instance_id=? ORDER BY id`, oldID)
	if err != nil {
		return err
	}
	for rs.Next() {
		var r cacheRow
		if err := rs.Scan(&r.id, &r.tokenID); err != nil {
			rs.Close()
			return err
		}
		rows = append(rows, r)
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return err
	}

	for _, r := range rows {
		var dupID int
		err := tx.QueryRow(
			`SELECT id FROM instance_cache WHERE token_id=? AND instance_id=?`,
			r.tokenID, newID).Scan(&dupID)
		switch {
		case err == nil && dupID != r.id:
			if err := mergeConfigToCacheID(tx, dupID, r.id); err != nil {
				return err
			}
			if _, err := tx.Exec(`DELETE FROM instance_cache WHERE id=?`, dupID); err != nil {
				return err
			}
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if _, err := tx.Exec(
			`UPDATE instance_cache SET instance_id=?, alive=1, stale=1 WHERE id=?`,
			newID, r.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// mergeConfigToCacheID moves per-instance configuration from one cache id to
// another when a repointed row collides with an already-cached new instance.
// The destination's existing configuration wins when both are present.
func mergeConfigToCacheID(tx *sql.Tx, fromID, toID int) error {
	stmts := []string{
		`INSERT INTO instance_scripts (cache_id, script)
		 SELECT ?, script FROM instance_scripts WHERE cache_id=?
		 ON CONFLICT(cache_id) DO NOTHING`,
		`INSERT INTO instance_root_passwords (cache_id, password)
		 SELECT ?, password FROM instance_root_passwords WHERE cache_id=?
		 ON CONFLICT(cache_id) DO NOTHING`,
		`INSERT INTO instance_auto_rebuild
		   (cache_id, user_id, token_id, enabled, port, threshold, interval_sec, updated_at)
		 SELECT ?, user_id, token_id, enabled, port, threshold, interval_sec, updated_at
		   FROM instance_auto_rebuild WHERE cache_id=?
		 ON CONFLICT(cache_id) DO NOTHING`,
		`INSERT INTO instance_failover
		   (cache_id, user_id, group_id, token_id, enabled, threshold, interval_sec, updated_at)
		 SELECT ?, user_id, group_id, token_id, enabled, threshold, interval_sec, updated_at
		   FROM instance_failover WHERE cache_id=?
		 ON CONFLICT(cache_id) DO NOTHING`,
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt, toID, fromID); err != nil {
			return err
		}
	}
	for _, table := range []string{"instance_scripts", "instance_root_passwords", "instance_auto_rebuild", "instance_failover"} {
		if _, err := tx.Exec(`DELETE FROM `+table+` WHERE cache_id=?`, fromID); err != nil {
			return err
		}
	}
	return nil
}

// AccountOptionsSnapshot is the cached /api/account/options payload for one
// token: regions, types and images as fetched from the Linode API.
type AccountOptionsSnapshot struct {
	Regions []linode.Region     `json:"regions"`
	Types   []linode.LinodeType `json:"types"`
	Images  []linode.Image      `json:"images"`
}

// CacheAccountOptions stores the options snapshot for a token, replacing any
// previous one. Called after a successful fetch so later reads can skip the
// upstream API.
func (s *Store) CacheAccountOptions(tokenID int, snap *AccountOptionsSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	regions, err := json.Marshal(orEmptyRegions(snap.Regions))
	if err != nil {
		return err
	}
	types, err := json.Marshal(orEmptyTypes(snap.Types))
	if err != nil {
		return err
	}
	images, err := json.Marshal(orEmptyImages(snap.Images))
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO account_options_cache (token_id, regions, types, images, fetched_at)
		 VALUES(?,?,?,?,?)
		 ON CONFLICT(token_id) DO UPDATE SET
			regions=excluded.regions, types=excluded.types,
			images=excluded.images, fetched_at=excluded.fetched_at`,
		tokenID, string(regions), string(types), string(images),
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}

// CachedAccountOptions returns the cached options snapshot for a token, or nil
// when nothing is cached.
func (s *Store) CachedAccountOptions(tokenID int) *AccountOptionsSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	var regions, types, images string
	err := s.db.QueryRow(
		`SELECT regions, types, images FROM account_options_cache WHERE token_id=?`, tokenID,
	).Scan(&regions, &types, &images)
	if err != nil {
		return nil
	}
	snap := &AccountOptionsSnapshot{}
	_ = json.Unmarshal([]byte(regions), &snap.Regions)
	_ = json.Unmarshal([]byte(types), &snap.Types)
	_ = json.Unmarshal([]byte(images), &snap.Images)
	if snap.Regions == nil {
		snap.Regions = []linode.Region{}
	}
	if snap.Types == nil {
		snap.Types = []linode.LinodeType{}
	}
	if snap.Images == nil {
		snap.Images = []linode.Image{}
	}
	return snap
}

func orEmptyRegions(in []linode.Region) []linode.Region {
	if in == nil {
		return []linode.Region{}
	}
	return in
}

func orEmptyTypes(in []linode.LinodeType) []linode.LinodeType {
	if in == nil {
		return []linode.LinodeType{}
	}
	return in
}

func orEmptyImages(in []linode.Image) []linode.Image {
	if in == nil {
		return []linode.Image{}
	}
	return in
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
		if plain, err := s.decryptSecret(t.Token); err == nil {
			t.Token = plain
		} else {
			t.Token = ""
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

// metaSecretLocked reads and decrypts one encrypted meta value.
func (s *Store) metaSecretLocked(key string) string {
	plain, err := s.decryptSecret(s.metaLocked(key))
	if err != nil {
		return ""
	}
	return plain
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
