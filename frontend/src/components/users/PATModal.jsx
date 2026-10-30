import { useCallback, useEffect, useState } from "react";
import { listUserTokens, addUserToken, deleteUserToken } from "../../api";
import Modal from "../Modal";

// ---------- PAT 管理模态框 ----------
export default function PATModal({ user, onClose }) {
  const [tokens, setTokens] = useState(null);
  const [label, setLabel] = useState("");
  const [token, setToken] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const res = await listUserTokens(user.id);
      setTokens(Array.isArray(res.tokens) ? res.tokens : []);
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
    } catch (err) {
      setError(err && err.message ? err.message : "删除失败");
    }
  }

  return (
    <Modal title="PAT 管理" onClose={onClose}>
      <p className="modal-detail">用户 PAT 管理（仅显示掩码，完整 token 不回显）。</p>
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
          />
          <input
            className="modal-input user-create-input"
            type="text"
            placeholder="Token"
            spellCheck={false}
            value={token}
            onChange={(e) => setToken(e.target.value)}
          />
          <button className="btn btn-primary" type="button" onClick={add} disabled={busy}>
            添加
          </button>
        </div>
      </div>
      <div className="modal-error">{error}</div>
      <div className="modal-actions">
        <button className="btn btn-ghost" type="button" onClick={onClose}>
          关闭
        </button>
      </div>
    </Modal>
  );
}
