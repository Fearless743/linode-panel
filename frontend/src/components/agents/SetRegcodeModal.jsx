import { useState } from "react";
import Modal from "../Modal";
import { useUi } from "../../ui";

// ---------- 设置注册码模态框 ----------
// onSetRegcode(code) 由页面编排（API 调用 + banner）；成功返回 true 时关闭。
export default function SetRegcodeModal({ onSetRegcode, onClose }) {
  const { showBanner } = useUi();
  const [regcode, setRegcode] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit() {
    if (!regcode.trim()) {
      showBanner("请输入注册码", false);
      return;
    }
    setBusy(true);
    try {
      const ok = await onSetRegcode(regcode);
      if (ok) onClose();
      else setBusy(false);
    } catch (err) {
      showBanner(err && err.message ? err.message : "设置失败", false);
      setBusy(false);
    }
  }

  return (
    <Modal title="设置注册码" onClose={onClose}>
      <p className="modal-detail">
        注册码用于派生 AES-256-GCM 密钥，加密下发给 Agent 的 PAT 只能被持有该码的
        Agent 解密。
      </p>
      <div className="modal-field">
        <label htmlFor="set-regcode-input">新注册码</label>
        <input
          id="set-regcode-input"
          className="modal-input regcode-input"
          type="text"
          placeholder="输入新注册码"
          spellCheck={false}
          value={regcode}
          onChange={(e) => setRegcode(e.target.value)}
        />
      </div>
      <div className="modal-actions">
        <button className="btn btn-ghost" type="button" onClick={onClose} disabled={busy}>
          取消
        </button>
        <button className="btn btn-primary" type="button" onClick={submit} disabled={busy}>
          {busy ? "设置中…" : "设置"}
        </button>
      </div>
    </Modal>
  );
}
