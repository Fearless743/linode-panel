import { useState } from "react";
import { useUi } from "../../ui";
import { createUser } from "../../api";
import Modal from "../Modal";
import UserEditModal from "./UserEditModal";

// ---------- 创建用户模态框 ----------
function CreateUserModal({ onClose, onCreated }) {
  const { showBanner } = useUi();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit() {
    if (!username.trim() || !password) {
      showBanner("用户名和密码不能为空", false);
      return;
    }
    setBusy(true);
    try {
      await createUser({ username: username.trim(), password, group_id: 1 });
      onCreated();
      onClose();
    } catch (err) {
      showBanner(err && err.message ? err.message : "创建用户失败", false);
      setBusy(false);
    }
  }

  return (
    <Modal title="创建用户" onClose={onClose}>
      <div className="modal-field">
        <label htmlFor="create-username">用户名</label>
        <input
          id="create-username"
          className="modal-input"
          type="text"
          autoComplete="off"
          value={username}
          onChange={(e) => setUsername(e.target.value)}
        />
      </div>
      <div className="modal-field">
        <label htmlFor="create-password">密码</label>
        <input
          id="create-password"
          className="modal-input"
          type="password"
          autoComplete="new-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
      </div>
      <div className="modal-actions">
        <button className="btn btn-ghost" type="button" onClick={onClose} disabled={busy}>
          取消
        </button>
        <button className="btn btn-primary" type="button" onClick={submit} disabled={busy}>
          创建
        </button>
      </div>
    </Modal>
  );
}

// ---------- 用户 tab ----------
export default function UsersTab({ users, groups, onReload }) {
  const [createOpen, setCreateOpen] = useState(false);
  const [editUser, setEditUser] = useState(null);

  const groupName = (id) => {
    const g = (groups || []).find((x) => x.id === id);
    return g ? g.name : "（未知组）";
  };

  return (
    <div className="users-tab-content">
      <div className="user-create">
        <div className="user-create-title">新建用户</div>
        <div className="user-create-row">
          <button className="btn btn-primary" type="button" onClick={() => setCreateOpen(true)}>
            创建用户
          </button>
        </div>
      </div>
      {users !== null && users.length === 0 && <div className="users-empty">暂无用户</div>}
      {users !== null && users.length > 0 && (
        <table className="users-table">
          <thead>
            <tr>
              <th>用户名</th>
              <th>组</th>
              <th>Token 数</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {users.map((u) => (
              <tr key={u.id}>
                <td>{u.username || ""}</td>
                <td>{groupName(u.group_id)}</td>
                <td>
                  {typeof u.token_count === "number"
                    ? u.token_count
                    : Array.isArray(u.tokens)
                      ? u.tokens.length
                      : 0}
                </td>
                <td>
                  <div className="actions">
                    <button className="action-btn" type="button" onClick={() => setEditUser(u)}>
                      编辑
                    </button>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {createOpen && (
        <CreateUserModal onClose={() => setCreateOpen(false)} onCreated={onReload} />
      )}
      {editUser && (
        <UserEditModal
          user={editUser}
          groups={groups}
          onClose={() => setEditUser(null)}
          onReload={onReload}
        />
      )}
    </div>
  );
}
