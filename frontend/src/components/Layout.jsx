import { NavLink, Outlet, useNavigate } from "react-router-dom";
import { useAuth } from "../auth";
import { useUi } from "../ui";

// 左侧导航：权限驱动导航 + 当前用户 + 登出。
function Sidebar() {
  const { user, logout, hasPerm } = useAuth();
  const nav = useNavigate();

  const links = [
    { to: "/", label: "实例", perm: "view" },
    { to: "/accounts", label: "账号管理", perm: null },
    { to: "/account-groups", label: "账号组", perm: null },
    { to: "/tasks", label: "重建任务", perm: "rebuild" },
    { to: "/users", label: "用户管理", perm: "admin" },
    { to: "/agents", label: "Agent", perm: "admin" },
  ];

  return (
    <aside className="sidebar">
      <div className="sidebar-header">
        <h1 className="app-title">Linode 开机面板</h1>
      </div>
      <nav className="sidebar-nav">
        {links
          .filter((l) => !l.perm || hasPerm(l.perm))
          .map((l) => (
            <NavLink
              key={l.to}
              to={l.to}
              end={l.to === "/"}
              className={({ isActive }) => "nav-link" + (isActive ? " active" : "")}
            >
              {l.label}
            </NavLink>
          ))}
      </nav>
      <div className="sidebar-footer">
        <span className="current-user">{user?.username}</span>
        <button
          className="btn btn-ghost"
          type="button"
          onClick={async () => {
            await logout();
            nav("/login", { replace: true });
          }}
        >
          登出
        </button>
      </div>
    </aside>
  );
}

// 全局 banner：由 useUi 渲染，无 banner 时不渲染。
function Banner() {
  const { banner } = useUi();
  if (!banner) return null;
  return (
    <div className="banner" role="alert">
      <span className="banner-text">{banner.msg}</span>
    </div>
  );
}

// 页面布局：左侧导航 + banner + 页面出口。
export default function Layout() {
  return (
    <div className="app-shell">
      <Sidebar />
      <div className="app-main">
        <Banner />
        <main className="page">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
