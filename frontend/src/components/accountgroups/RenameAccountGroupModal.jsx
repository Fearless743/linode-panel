import { useState } from "react";
import Modal from "../Modal";
import { renameAccountGroup } from "../../api";

// ---------- 账号组改名模态框 ----------
export default function RenameAccountGroupModal({ group, onClose, onRenamed }) {
  const [name, setName] = useState(group.name || "");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit() {
    const trimmed = name.trim();
    if (!trimmed) {
      setError("组名称不能为空");
      return;
    }
    if (trimmed === (group.name || "")) {
      onClose();
      return;
    }
    setBusy(true);
    setError("");
    try {
      await renameAccountGroup(group.id, trimmed);
      onRenamed();
      onClose();
    } catch (err) {
      setError(err && err.message ? err.message : "重命名失败");
      setBusy(false);
    }
  }

  return (
    <Modal title="重命名账号组" onClose={onClose}>
      <div className="modal-field">
        <label htmlFor="ag-rename-name">组名称</label>
        <input
          id="ag-rename-name"
          className="modal-input"
          type="text"
          autoComplete="off"
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
          {busy ? "保存中…" : "保存"}
        </button>
      </div>
    </Modal>
  );
}
