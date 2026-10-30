import { useState } from "react";
import Modal from "../Modal";
import { createAccountGroup } from "../../api";

// ---------- 新建账号组模态框 ----------
export default function CreateAccountGroupModal({ onClose, onCreated }) {
  const [name, setName] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit() {
    const trimmed = name.trim();
    if (!trimmed) {
      setError("组名称不能为空");
      return;
    }
    setBusy(true);
    setError("");
    try {
      await createAccountGroup(trimmed);
      onCreated();
      onClose();
    } catch (err) {
      setError(err && err.message ? err.message : "创建失败");
      setBusy(false);
    }
  }

  return (
    <Modal title="新建账号组" onClose={onClose}>
      <p className="modal-detail">
        把一个或多个账号（PAT）归入同一账号组；组内某账号的实例被封禁或余额不足时，可用相同配置
        在组内另一账号重建替换。
      </p>
      <div className="modal-field">
        <label htmlFor="ag-create-name">组名称</label>
        <input
          id="ag-create-name"
          className="modal-input"
          type="text"
          autoComplete="off"
          placeholder="名称（如 主备账号组）"
          value={name}
          onChange={(e) => setName(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") submit();
          }}
          disabled={busy}
        />
      </div>
      <div className="modal-error">{error}</div>
      <div className="modal-actions">
        <button className="btn btn-ghost" type="button" onClick={onClose} disabled={busy}>
          取消
        </button>
        <button className="btn btn-primary" type="button" onClick={submit} disabled={busy}>
          {busy ? "创建中…" : "创建"}
        </button>
      </div>
    </Modal>
  );
}
