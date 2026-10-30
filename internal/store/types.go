// Package store persists panel users, permission groups, per-user Linode
// PATs, and per-instance boot scripts in a SQLite database, plus (in-memory)
// login sessions.
//
// Frozen contract — implement in store.go, do not change signatures:
//
//	func Open(path string) (*Store, error)
//	func (s *Store) Save() error
//	func HashPassword(p string) (string, error)
//	func CheckPasswordHash(hash, p string) bool
//	// users
//	func (s *Store) Users() []User
//	func (s *Store) User(id int) (User, bool)
//	func (s *Store) UserByName(name string) (User, bool)
//	func (s *Store) CreateUser(username, password string, groupID int) (User, error)
//	func (s *Store) UpdateUser(id, groupID int, newPassword string) error
//	func (s *Store) DeleteUser(id int) error
//	func (s *Store) CheckPassword(username, password string) (User, error)
//	// tokens (per user)
//	func (s *Store) TokensOf(userID int) []Token
//	func (s *Store) AddToken(userID int, label, token string) (Token, error)
//	func (s *Store) DeleteToken(userID, tokenID int) error
//	func (s *Store) TokenPAT(userID, tokenID int) (string, error)
//	func (s *Store) SetTokenAgent(userID, tokenID, agentID int) error // agentID 0 解绑
//	// panel self-agent custom name
//	func (s *Store) SelfAgentName() string
//	func (s *Store) SetSelfAgentName(name string) error
//	// groups
//	func (s *Store) Groups() []Group
//	func (s *Store) Group(id int) (Group, bool)
//	func (s *Store) CreateGroup(name string, perms []string) (Group, error)
//	func (s *Store) UpdateGroup(id int, name string, perms []string) error
//	func (s *Store) DeleteGroup(id int) error
//	// sessions (in memory)
//	func (s *Store) NewSession(userID int, ttl time.Duration) string
//	func (s *Store) SessionUser(cookieValue string) (User, bool)
//	func (s *Store) DropSession(cookieValue string)
//	// perms
//	func (s *Store) PermsOf(userID int) []string
//	// agent registration code
//	func (s *Store) RegCode() string
//	func (s *Store) SetRegCode(code string) error
//	// per-instance boot scripts
//	func (s *Store) InstanceScript(instanceID int) (string, bool)
//	func (s *Store) SetInstanceScript(instanceID int, script string) error
//	func (s *Store) DeleteInstanceScript(instanceID int) error
//	func (s *Store) InstanceScriptIDs() []int
//	// per-token instance cache
//	func (s *Store) CacheInstances(tokenID int, insts []linode.Instance) error
//	func (s *Store) CachedInstances(tokenID int) []linode.Instance
//	func (s *Store) InvalidateInstanceCache(tokenID int) error
//	func (s *Store) UpsertInstanceCache(tokenID int, inst linode.Instance) error
package store

import "time"

// Permission names (frozen). PermView: see instances/SSH info; PermPower:
// boot/shutdown/reboot; PermRebuild: delete+recreate & view rebuild tasks;
// PermScript: read/write per-instance boot scripts; PermAdmin: manage users
// & groups.
const (
	PermView    = "view"
	PermPower   = "power"
	PermRebuild = "rebuild"
	PermScript  = "script"
	PermAdmin   = "admin"
)

// ValidPerms returns all permission names in display order.
func ValidPerms() []string {
	return []string{PermView, PermPower, PermRebuild, PermScript, PermAdmin}
}

// SessionTTL is the default login session lifetime.
const SessionTTL = 7 * 24 * time.Hour

// Token is one Linode Personal Access Token belonging to a user.
type Token struct {
	ID    int    `json:"id"`
	Label string `json:"label"`
	Token string `json:"token"` // stored as-is; the database must be 0600
	// AgentID is the agent bound to this account (0 = unbound). When set,
	// via-agent operations for this account are routed to that agent.
	AgentID int `json:"agent_id"`
}

// User is a panel user with a bcrypt password hash, a permission group,
// and any number of PATs.
type User struct {
	ID       int     `json:"id"`
	Username string  `json:"username"`
	PwdHash  string  `json:"pwd_hash"`
	GroupID  int     `json:"group_id"`
	Tokens   []Token `json:"tokens"`
}

// Group is a named set of permissions.
type Group struct {
	ID    int      `json:"id"`
	Name  string   `json:"name"`
	Perms []string `json:"perms"`
}

// AccountGroup 是一个命名的账号组：把同一 user 下的多个账号(Token)绑定在一起，
// 当组内某账号的实例被封号或没余额时，可用相同配置在组内另一账号重建替换。
// 与权限组 Group 无关。
type AccountGroup struct {
	ID        int    `json:"id"`
	UserID    int    `json:"user_id"`
	Name      string `json:"name"`
	TokenIDs  []int  `json:"token_ids"`  // 组内账号(Token) id，升序
	CreatedAt string `json:"created_at"` // RFC3339
}

// Masked renders a token for display: last 4 chars visible.
func Masked(t string) string {
	if len(t) <= 4 {
		return "****"
	}
	return "****" + t[len(t)-4:]
}
