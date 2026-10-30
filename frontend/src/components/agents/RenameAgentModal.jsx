import { useState } from "react";
import Modal from "../Modal";
import { useUi } from "../../ui";

// ---------- 重命名本地面板 Agent 模态框 ----------
// onRename(agent, name) 由页面编排（API 调用 + 刷新）；成功返回 true 时关闭。
export default function RenameAgentModal({ agent, onRename, onClose }) {
  const { showBanner } = useUi();
  const [name, setName] = useState(agent ? agent.name : "");
  const [busy, setBusy] = useState(false);

  async function submit() {
    if (!name.trim()) {
      showBanner("请输入 Agent 名称", false);
      return;
    }
    setBusy(true);
    try {
      const ok = await onRename(agent, name);
      if (ok) onClose();
      else setBusy(false);
    } catch (err) {
      showBanner(err && err.message ? err.message : "重命名失败", false);
      setBusy(false);
    }
  }

  return (
    <Modal title="重命名" onClose={onClose}>
      <div className="modal-field">
        <label htmlFor="rename-agent-name">Agent 名称</label>
        <input
          id="rename-agent-name"
          className="modal-input"
          type="text"
          placeholder="如 本机-linode"
          spellCheck={false}
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
      </div>
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
