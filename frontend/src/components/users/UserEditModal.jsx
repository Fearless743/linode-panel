import { useCallback, useEffect, useId, useState } from "react";
import {
  addUserToken,
  deleteUser,
  deleteUserToken,
  listUserTokens,
  updateUser,
} from "../../api";
import Modal from "../Modal";

// ---------- 基本信息区块（权限组 / 重置密码） ----------
function BasicSection({ user, groups, onSaved }) {
  const idPrefix = useId();
  const [groupId, setGroupId] = useState(
    user.group_id != null ? String(user.group_id) : ""
  );
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState(null); // { msg, cls }

  async function save() {
    if (!groupId) {
      setStatus({ msg: "请选择权限组", cls: "error" });
      return;
    }
    if (password && password.length < 6) {
      setStatus({ msg: "密码长度至少 6 位", cls: "error" });
      return;
    }
    setBusy(true);
    setStatus({ msg: "保存中…", cls: "" });
    try {
      // password 为空表示不修改，后端会忽略空密码。
      await updateUser(user.id, { group_id: parseInt(groupId, 10), password });
      setPassword("");
      setStatus({ msg: "已保存", cls: "ok" });
      if (onSaved) onSaved();
    } catch (err) {
      setStatus({ msg: "保存失败：" + ((err && err.message) || "未知错误"), cls: "error" });
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <div className="modal-field">
        <label htmlFor={idPrefix + "-group"}>权限组</label>
        <select
          id={idPrefix + "-group"}
          className="modal-input"
          value={groupId}
          onChange={(e) => setGroupId(e.target.value)}
          disabled={busy}
        >
          <option value="">请选择权限组</option>
          {(groups || []).map((g) => (
            <option key={g.id} value={String(g.id)}>
              ({g.id}) {g.name}
            </option>
          ))}
        </select>
      </div>
      <div className="modal-field">
        <label htmlFor={idPrefix + "-password"}>重置密码</label>
        <input
          id={idPrefix + "-password"}
          className="modal-input"
          type="password"
          autoComplete="new-password"
          placeholder="留空表示不修改（至少 6 位）"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          disabled={busy}
          onKeyDown={(e) => {
            if (e.key === "Enter") save();
          }}
        />
      </div>
      <div className="modal-actions">
        <span className={"boot-script-status" + (status ? " " + status.cls : "")}>
          {status ? status.msg : ""}
        </span>
        <button className="btn btn-primary" type="button" onClick={save} disabled={busy}>
          保存
        </button>
      </div>
    </>
  );
}

// ---------- PAT 区块 ----------
function PATSection({ user, onChanged }) {
  const [tokens, setTokens] = useState(null);
  const [label, setLabel] = useState("");
  const [token, setToken] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const res = await listUserTokens(user.id);
      setTokens(Array.isArray(res.tokens) ? res.tokens : []);
      setError("");
    } catch (err) {
      setTokens([]);
      setError(err && err.message ? err.message : "加载失败");
    }
  }, [user.id]);

  useEffect(() => {
    load();
  }, [load]);

  async function add() {
    if (!label.trim() || !token.trim()) {
      setError("标签和 Token 不能为空");
      return;
    }
    setBusy(true);
    setError("");
    try {
      await addUserToken(user.id, { label: label.trim(), token: token.trim() });
      setLabel("");
      setToken("");
      await load();
      if (onChanged) onChanged();
    } catch (err) {
      setError(err && err.message ? err.message : "添加失败");
    } finally {
      setBusy(false);
    }
  }

  async function remove(t) {
    try {
      await deleteUserToken(user.id, t.id);
      await load();
      if (onChanged) onChanged();
    } catch (err) {
      setError(err && err.message ? err.message : "删除失败");
    }
  }

  return (
    <>
      <p className="modal-detail">该用户的 PAT（仅显示掩码，完整 token 不回显）。</p>
      <div className="pat-list">
        {tokens === null && <div className="users-empty">正在加载…</div>}
        {tokens !== null && tokens.length === 0 && <div className="users-empty">暂无 PAT</div>}
        {tokens !== null &&
          tokens.map((t) => (
            <div className="pat-item" key={t.id}>
              <div className="pat-item-info">
                <div className="pat-item-label">{t.label || "（未命名）"}</div>
                <div className="pat-item-masked">{t.masked || ""}</div>
              </div>
              <button className="action-btn danger" type="button" onClick={() => remove(t)}>
                删除
              </button>
            </div>
          ))}
      </div>
      <div className="user-create">
        <div className="user-create-title">添加 PAT</div>
        <div className="user-create-row">
          <input
            className="modal-input user-create-input"
            type="text"
            placeholder="标签"
            value={label}
            onChange={(e) => setLabel(e.target.value)}
            disabled={busy}
          />
          <input
            className="modal-input user-create-input"
            type="text"
            placeholder="Token"
            spellCheck={false}
            value={token}
            onChange={(e) => setToken(e.target.value)}
            disabled={busy}
          />
          <button className="btn btn-primary" type="button" onClick={add} disabled={busy}>
            添加
          </button>
        </div>
      </div>
      <div className="modal-error">{error}</div>
    </>
  );
}

// ---------- 用户「编辑」模态框：改组、重置密码、PAT 管理、删除用户 ----------
export default function UserEditModal({ user, groups, onClose, onReload }) {
  const [tab, setTab] = useState("basic");
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [error, setError] = useState("");

  async function doDelete() {
    setDeleting(true);
    setError("");
    try {
      await deleteUser(user.id);
      setConfirmDelete(false);
      await onReload?.();
      onClose();
    } catch (err) {
      setDeleting(false);
      setConfirmDelete(false);
      setError(err && err.message ? err.message : "删除用户失败");
    }
  }

  return (
    <Modal title={"编辑用户 · " + (user.username || "")} onClose={onClose}>
      <div className="edit-modal-tabs">
        <button
          type="button"
          className={"edit-modal-tab" + (tab === "basic" ? " active" : "")}
          onClick={() => setTab("basic")}
        >
          基本信息
        </button>
        <button
          type="button"
          className={"edit-modal-tab" + (tab === "pat" ? " active" : "")}
          onClick={() => setTab("pat")}
        >
          PAT
        </button>
      </div>
      <div style={{ display: tab === "basic" ? "block" : "none" }}>
        <BasicSection user={user} groups={groups} onSaved={onReload} />
      </div>
      <div style={{ display: tab === "pat" ? "block" : "none" }}>
        <PATSection user={user} onChanged={onReload} />
      </div>
      <div className="modal-error">{error}</div>
      {confirmDelete && (
        <div className="user-create">
          <div className="user-create-title">删除用户</div>
          <p className="modal-detail">
            确定删除用户 {user.username || ""}？该用户的 PAT 也会一并删除，操作不可恢复。
          </p>
          <div className="modal-actions">
            <button
              className="btn btn-ghost"
              type="button"
              onClick={() => setConfirmDelete(false)}
              disabled={deleting}
            >
              取消
            </button>
            <button className="btn btn-danger" type="button" onClick={doDelete} disabled={deleting}>
              {deleting ? "删除中…" : "确认删除"}
            </button>
          </div>
        </div>
      )}
      <div className="modal-actions">
        <button
          className="btn btn-danger"
          type="button"
          onClick={() => setConfirmDelete(true)}
          disabled={deleting || confirmDelete}
          style={{ marginRight: "auto" }}
        >
          删除用户
        </button>
        <button className="btn btn-ghost" type="button" onClick={onClose} disabled={deleting}>
          关闭
        </button>
      </div>
    </Modal>
  );
}
