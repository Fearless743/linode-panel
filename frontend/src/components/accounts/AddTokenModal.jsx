import { useState } from "react";
import Modal from "../Modal";
import { jsonRequest } from "../../api";

// ---------- 绑定新账号（添加 Token）模态框 ----------
export default function AddTokenModal({ onClose, onAdded }) {
  const [label, setLabel] = useState("");
  const [token, setToken] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function add() {
    if (!label.trim() || !token.trim()) {
      setError("名称和 Token 不能为空");
      return;
    }
    setBusy(true);
    setError("");
    try {
      await jsonRequest("/api/account/tokens", "POST", {
        label: label.trim(),
        token: token.trim(),
      });
      onAdded();
      onClose();
    } catch (err) {
      setError(err && err.message ? err.message : "添加失败");
      setBusy(false);
    }
  }

  return (
    <Modal title="绑定新账号" onClose={onClose}>
      <p className="modal-detail">
        每个账号对应一个 Linode PAT；绑定后可用于聚合实例、开关机与创建实例。
      </p>
      <div className="modal-field">
        <label htmlFor="add-token-label">名称</label>
        <input
          id="add-token-label"
          className="modal-input"
          type="text"
          placeholder="名称（如 生产账号）"
          value={label}
          onChange={(e) => setLabel(e.target.value)}
        />
      </div>
      <div className="modal-field">
        <label htmlFor="add-token-input">Token</label>
        <input
          id="add-token-input"
          className="modal-input"
          type="text"
          placeholder="Linode PAT"
          spellCheck={false}
          value={token}
          onChange={(e) => setToken(e.target.value)}
        />
      </div>
      <div className="modal-error">{error}</div>
      <div className="modal-actions">
        <button className="btn btn-ghost" type="button" onClick={onClose} disabled={busy}>
          取消
        </button>
        <button className="btn btn-primary" type="button" onClick={add} disabled={busy}>
          {busy ? "添加中…" : "添加"}
        </button>
      </div>
    </Modal>
  );
}
