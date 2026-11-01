import { useCallback, useEffect, useState } from "react";
import { listUsers, listGroups } from "../api";
import UsersTab from "../components/users/UsersTab";
import GroupsTab from "../components/users/GroupsTab";
import GroupEditModal from "../components/users/GroupEditModal";

// ---------- 页面：仅保留 tab 状态与数据编排 ----------
export default function Users() {
  const [tab, setTab] = useState("users");
  const [users, setUsers] = useState(null);
  const [groups, setGroups] = useState(null);
  const [error, setError] = useState("");
  const [editGroup, setEditGroup] = useState(null);

  const load = useCallback(async () => {
    try {
      const [u, g] = await Promise.all([listUsers(), listGroups()]);
      setUsers(Array.isArray(u.users) ? u.users : []);
      setGroups(Array.isArray(g.groups) ? g.groups : []);
      setError("");
    } catch (err) {
      setUsers([]);
      setGroups([]);
      setError(err && err.message ? err.message : "未知错误");
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  return (
    <section className="panel">
      <h2 className="panel-title">用户管理</h2>
      <div className="users-tabs">
        <button
          className={"btn btn-ghost users-tab" + (tab === "users" ? " active" : "")}
          type="button"
          onClick={() => setTab("users")}
        >
          用户
        </button>
        <button
          className={"btn btn-ghost users-tab" + (tab === "groups" ? " active" : "")}
          type="button"
          onClick={() => setTab("groups")}
        >
          权限组
        </button>
      </div>

      {error && <div className="users-empty">加载失败：{error}</div>}

      {tab === "users" && <UsersTab users={users} groups={groups} onReload={load} />}

      {tab === "groups" && (
        <GroupsTab
          groups={groups}
          users={users}
          onEditGroup={setEditGroup}
          onReload={load}
        />
      )}

      {editGroup && (
        <GroupEditModal
          group={editGroup}
          onClose={() => setEditGroup(null)}
          onSaved={load}
        />
      )}
    </section>
  );
}
