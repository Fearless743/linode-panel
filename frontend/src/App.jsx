import {
  BrowserRouter,
  Routes,
  Route,
  Navigate,
  Outlet,
  useLocation,
} from "react-router-dom";
import { lazy, Suspense, useEffect } from "react";
import { useAuth } from "./auth";
import Layout from "./components/Layout";

// 页面懒加载：每页单独 chunk，按需加载。
const Login = lazy(() => import("./pages/Login"));
const Instances = lazy(() => import("./pages/Instances"));
const SSHKeys = lazy(() => import("./pages/SSHKeys"));
const Tasks = lazy(() => import("./pages/Tasks"));
const Users = lazy(() => import("./pages/Users"));
const Agents = lazy(() => import("./pages/Agents"));
const Accounts = lazy(() => import("./pages/Accounts"));

// 路由路径 → 页面名（用于 document.title）。
const PAGE_TITLES = {
  "/login": "登录",
  "/": "实例",
  "/accounts": "账号管理",
  "/ssh-keys": "SSH 公钥",
  "/tasks": "重建任务",
  "/users": "用户管理",
  "/agents": "Agent",
};

// 路由守卫：根据当前路径更新页面标题。
function PageTitle() {
  const { pathname } = useLocation();
  useEffect(() => {
    const name = PAGE_TITLES[pathname];
    document.title = name ? `${name} · Linode 开机面板` : "Linode 开机面板";
  }, [pathname]);
  return null;
}

// 未登录 → 登录页。
function RequireAuth() {
  const { user, loading } = useAuth();
  if (loading) return <div className="page-status">正在加载…</div>;
  if (!user) return <Navigate to="/login" replace />;
  return <Outlet />;
}

export default function App() {
  return (
    <BrowserRouter>
      <PageTitle />
      <Suspense fallback={<div className="page-status">加载中…</div>}>
        <Routes>
          <Route path="/login" element={<Login />} />
          <Route element={<RequireAuth />}>
            <Route element={<Layout />}>
              <Route index element={<Instances />} />
              <Route path="/ssh-keys" element={<SSHKeys />} />
              <Route path="/tasks" element={<Tasks />} />
              <Route path="/users" element={<Users />} />
              <Route path="/agents" element={<Agents />} />
              <Route path="/accounts" element={<Accounts />} />
            </Route>
          </Route>
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </Suspense>
    </BrowserRouter>
  );
}
