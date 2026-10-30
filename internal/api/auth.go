package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"linode-panel/internal/store"
)

// contextKey is the private key type for the authenticated user in the
// request context.
type contextKey struct{}

var userKey = contextKey{}

// UserFrom returns the authenticated user stored in ctx by requireAuth.
// The bool reports whether a user was present.
func UserFrom(ctx context.Context) (store.User, bool) {
	u, ok := ctx.Value(userKey).(store.User)
	return u, ok
}

// sessionCookie is the login cookie name (frozen by the web UI).
const sessionCookie = "panel_session"

// requireAuth gates h behind a logged-in user with the given permission.
// Missing/expired session -> 401; insufficient permission -> 403.
func (s *Server) requireAuth(perm string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil || c.Value == "" {
			writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "未登录或会话已过期"})
			return
		}
		user, ok := s.Store.SessionUser(c.Value)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "未登录或会话已过期"})
			return
		}
		if perm != "" && !hasPerm(s.Store.PermsOf(user.ID), perm) {
			writeJSON(w, http.StatusForbidden, errorResponse{Error: "权限不足"})
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), userKey, user)))
	}
}

// hasPerm reports whether perms contains perm.
func hasPerm(perms []string, perm string) bool {
	for _, p := range perms {
		if p == perm {
			return true
		}
	}
	return false
}

// handleLogin verifies credentials, creates a session, and sets the cookie.
// Failures always return a uniform 401.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Username == "" || req.Password == "" {
		writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "用户名或密码错误"})
		return
	}
	user, err := s.Store.CheckPassword(req.Username, req.Password)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "用户名或密码错误"})
		return
	}
	session := s.Store.NewSession(user.ID, store.SessionTTL)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    session,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(store.SessionTTL / time.Second),
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"username": user.Username,
		"perms":    s.Store.PermsOf(user.ID),
	})
}

// handleLogout drops the current session and clears the cookie.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		s.Store.DropSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

// handleMe returns the current user and permissions.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "未登录或会话已过期"})
		return
	}
	user, ok := s.Store.SessionUser(c.Value)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "未登录或会话已过期"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"username": user.Username,
		"perms":    s.Store.PermsOf(user.ID),
	})
}

// resolveToken resolves the request's token_id to a plaintext PAT for user.
// An empty tokenID selects the user's first PAT; an unknown id returns an
// error suitable for a 400 response.
func (s *Server) resolveToken(user store.User, tokenID string) (string, error) {
	toks := s.Store.TokensOf(user.ID)
	if len(toks) == 0 {
		return "", errNoToken
	}
	if tokenID == "" {
		return toks[0].Token, nil
	}
	id, err := parseTokenID(tokenID)
	if err != nil {
		return "", errNoToken
	}
	for _, t := range toks {
		if t.ID == id {
			return t.Token, nil
		}
	}
	return "", errNoToken
}

var errNoToken = errors.New("PAT 不存在")

// tokenIDFor returns the DB id of the token selected by tokenID (empty = the
// user's first PAT), or 0 when it cannot be resolved. Used to invalidate the
// per-token instance cache after mutating operations.
func (s *Server) tokenIDFor(user store.User, tokenID string) int {
	toks := s.Store.TokensOf(user.ID)
	if len(toks) == 0 {
		return 0
	}
	if tokenID == "" {
		return toks[0].ID
	}
	id, err := parseTokenID(tokenID)
	if err != nil {
		return 0
	}
	for _, t := range toks {
		if t.ID == id {
			return t.ID
		}
	}
	return 0
}

func parseTokenID(s string) (int, error) {
	var id int
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errNoToken
		}
		id = id*10 + int(c-'0')
	}
	if id <= 0 {
		return 0, errNoToken
	}
	return id, nil
}
