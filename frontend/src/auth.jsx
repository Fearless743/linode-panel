// 登录态与权限：AuthProvider 在应用根部提供 user / perms / hasPerm。
import { createContext, useCallback, useContext, useEffect, useMemo, useState } from "react";
import { authMe, authLogin, authLogout, setUnauthorizedHandler } from "./api";

const AuthContext = createContext(null);

// 将后端返回的用户数据规范化为 { username, perms: string[] }。
// fallbackUsername：登录时后端未回传 username 时回退为表单输入的账号。
function normalizeUser(data, fallbackUsername) {
  return {
    username: (data && data.username) || fallbackUsername || "",
    perms: Array.isArray(data && data.perms) ? data.perms : [],
  };
}

export function AuthProvider({ children }) {
  // user = { username, perms: string[] } | null
  const [user, setUser] = useState(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    // 任意请求 401 时自动回到未登录态（由 api.js 触发）。
    setUnauthorizedHandler(() => setUser(null));
    authMe()
      .then((data) => setUser(normalizeUser(data)))
      .catch(() => setUser(null))
      .finally(() => setLoading(false));
  }, []);

  const login = useCallback(async (username, password) => {
    // 失败时 api.js 抛出 ApiError（err.message 可读），由调用方（Login）展示。
    const data = await authLogin(username, password);
    setUser(normalizeUser(data, username));
    return data;
  }, []);

  const logout = useCallback(async () => {
    try {
      await authLogout();
    } catch {
      // 即使登出请求失败也回到未登录态。
    }
    setUser(null);
  }, []);

  const hasPerm = useCallback(
    (perm) => !!user && user.perms.includes(perm),
    [user]
  );

  const value = useMemo(
    () => ({ user, loading, login, logout, hasPerm }),
    [user, loading, login, logout, hasPerm]
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth() {
  return useContext(AuthContext);
}
