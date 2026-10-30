import { useState } from "react";
import { useUi } from "../../ui";
import { createUser, updateUser, deleteUser } from "../../api";
import Modal from "../Modal";
import ConfirmDialog from "../ui/ConfirmDialog";

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

// ---------- 重置密码模态框 ----------
function ChangePasswordModal({ user, onClose, onChanged }) {
  const { showBanner } = useUi();
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit() {
    if (!password) {
      showBanner("密码不能为空", false);
      return;
    }
    setBusy(true);
    try {
      await updateUser(user.id, { password });
      onChanged();
      onClose();
    } catch (err) {
      showBanner(err && err.message ? err.message : "操作失败", false);
      setBusy(false);
    }
  }

  return (
    <Modal title={"重置密码 · " + (user.username || "")} onClose={onClose}>
      <div className="modal-field">
        <label htmlFor="change-pw-input">新密码</label>
        <input
          id="change-pw-input"
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
          保存
        </button>
      </div>
    </Modal>
  );
}

// ---------- 改组模态框 ----------
function ChangeGroupModal({ user, groups, onClose, onChanged }) {
  const { showBanner } = useUi();
  const [groupId, setGroupId] = useState(
    user.group_id != null ? String(user.group_id) : ""
  );
  const [busy, setBusy] = useState(false);

  async function submit() {
    const gid = parseInt(groupId, 10);
    if (isNaN(gid)) {
      showBanner("请选择有效的权限组", false);
      return;
    }
    setBusy(true);
    try {
      await updateUser(user.id, { group_id: gid });
      onChanged();
      onClose();
    } catch (err) {
      showBanner(err && err.message ? err.message : "操作失败", false);
      setBusy(false);
    }
  }

  return (
    <Modal title={"改组 · " + (user.username || "")} onClose={onClose}>
      <div className="modal-field">
        <label htmlFor="change-group-select">目标权限组</label>
        <select
          id="change-group-select"
          className="modal-input"
          value={groupId}
          onChange={(e) => setGroupId(e.target.value)}
        >
          {(groups || []).map((g) => (
            <option key={g.id} value={String(g.id)}>
              {"(" + g.id + ") " + g.name}
            </option>
          ))}
        </select>
      </div>
      <div className="modal-actions">
        <button className="btn btn-ghost" type="button" onClick={onClose} disabled={busy}>
          取消
        </button>
        <button className="btn btn-primary" type="button" onClick={submit} disabled={busy}>
          保存
        </button>
      </div>
    </Modal>
  );
}

// ---------- 用户 tab ----------
export default function UsersTab({ users, groups, onOpenPat, onReload }) {
  const { showBanner } = useUi();
  const [createOpen, setCreateOpen] = useState(false);
  const [pwUser, setPwUser] = useState(null);
  const [groupUser, setGroupUser] = useState(null);
  const [delUser, setDelUser] = useState(null);

  const groupName = (id) => {
    const g = (groups || []).find((x) => x.id === id);
    return g ? g.name : "（未知组）";
  };

  async function doDelete() {
    if (!delUser) return;
    try {
      await deleteUser(delUser.id);
      setDelUser(null);
      await onReload();
    } catch (err) {
      setDelUser(null);
      showBanner(err && err.message ? err.message : "删除用户失败", false);
    }
  }

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
                    <button className="action-btn" type="button" onClick={() => onOpenPat(u)}>
                      PAT
                    </button>
                    <button className="action-btn" type="button" onClick={() => setPwUser(u)}>
                      改密
                    </button>
                    <button className="action-btn" type="button" onClick={() => setGroupUser(u)}>
                      改组
                    </button>
                    <button className="action-btn danger" type="button" onClick={() => setDelUser(u)}>
                      删除
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
      {pwUser && (
        <ChangePasswordModal
          user={pwUser}
          onClose={() => setPwUser(null)}
          onChanged={onReload}
        />
      )}
      {groupUser && (
        <ChangeGroupModal
          user={groupUser}
          groups={groups}
          onClose={() => setGroupUser(null)}
          onChanged={onReload}
        />
      )}
      {delUser && (
        <ConfirmDialog
          title="删除用户"
          message={"确定删除用户 " + delUser.username + " ？"}
          confirmText="删除"
          danger
          onConfirm={doDelete}
          onCancel={() => setDelUser(null)}
        />
      )}
    </div>
  );
}
