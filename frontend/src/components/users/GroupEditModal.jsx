import { useState } from "react";
import { updateGroup } from "../../api";
import Modal from "../Modal";
import { PERM_LABELS } from "../../constants";

// ---------- 权限组编辑模态框 ----------
export default function GroupEditModal({ group, onClose, onSaved }) {
  const [name, setName] = useState(group.name || "");
  const [perms, setPerms] = useState(Array.isArray(group.perms) ? group.perms : []);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  function toggle(perm) {
    setPerms((p) => (p.includes(perm) ? p.filter((x) => x !== perm) : [...p, perm]));
  }

  async function save() {
    if (!name.trim()) {
      setError("组名称不能为空");
      return;
    }
    setBusy(true);
    setError("");
    try {
      await updateGroup(group.id, { name: name.trim(), perms });
      onSaved();
      onClose();
    } catch (err) {
      setError(err && err.message ? err.message : "更新权限组失败");
      setBusy(false);
    }
  }

  return (
    <Modal title="编辑权限组" onClose={onClose}>
      <div className="modal-field">
        <label htmlFor="edit-group-name">组名称</label>
        <input
          id="edit-group-name"
          className="modal-input"
          type="text"
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
      </div>
      <div className="modal-field">
        <label>权限</label>
        <div className="perm-checks">
          {PERM_LABELS.map(([p, text]) => (
            <label className="perm-check" key={p}>
              <input
                type="checkbox"
                value={p}
                checked={perms.includes(p)}
                onChange={() => toggle(p)}
              />
              {text}
            </label>
          ))}
        </div>
      </div>
      <div className="modal-actions">
        <button className="btn btn-ghost" type="button" onClick={onClose} disabled={busy}>
          取消
        </button>
        <button className="btn btn-primary" type="button" onClick={save} disabled={busy}>
          保存
        </button>
      </div>
      <div className="modal-error">{error}</div>
    </Modal>
  );
}
