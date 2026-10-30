import { useState } from "react";
import Modal from "../Modal";
import { useUi } from "../../ui";

// ---------- 新增 Agent 模态框 ----------
// onCreate(name) 由页面编排（API 调用 + 刷新）；成功返回 true 时关闭。
export default function CreateAgentModal({ onCreate, onClose }) {
  const { showBanner } = useUi();
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit() {
    if (!name.trim()) {
      showBanner("请输入 Agent 名称", false);
      return;
    }
    setBusy(true);
    try {
      const ok = await onCreate(name);
      if (ok) onClose();
      else setBusy(false);
    } catch (err) {
      showBanner(err && err.message ? err.message : "创建失败", false);
      setBusy(false);
    }
  }

  return (
    <Modal title="新增 Agent" onClose={onClose}>
      <div className="modal-field">
        <label htmlFor="create-agent-name">Agent 名称</label>
        <input
          id="create-agent-name"
          className="modal-input"
          type="text"
          placeholder="Agent 名称（如 本机-linode）"
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
          {busy ? "创建中…" : "创建"}
        </button>
      </div>
    </Modal>
  );
}
