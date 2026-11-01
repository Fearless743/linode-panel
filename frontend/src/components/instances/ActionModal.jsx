// 操作确认模态框：确认开机/关机/重启。
// 执行方式固定跟随账号绑定：账号绑定了 Agent 就经该 Agent 执行，否则面板直发。
import { useState } from "react";
import { ACTION_LABELS } from "../../constants";
import Modal from "../Modal";
import { Button } from "../ui";

export default function ActionModal({ inst, action, boundAgentName, onClose, onConfirm }) {
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function confirm() {
    setError("");
    setBusy(true);
    try {
      await onConfirm();
      onClose();
    } catch (err) {
      setError(err && err.message ? err.message : "操作失败");
      setBusy(false);
    }
  }

  const label = ACTION_LABELS[action] || action;
  const channel = boundAgentName
    ? "经 Agent「" + boundAgentName + "」执行"
    : "面板直发执行";

  return (
    <Modal title={"确认" + label} onClose={onClose}>
      <p className="modal-detail">
        将对实例「{inst.label || "#" + inst.id}」执行{label}操作。
      </p>
      <p className="modal-detail">执行方式：{channel}（跟随账号绑定）</p>
      <div className="modal-actions">
        <Button variant="ghost" onClick={onClose} disabled={busy}>
          取消
        </Button>
        <Button variant="primary" onClick={confirm} disabled={busy}>
          确认
        </Button>
      </div>
      <div className="modal-error">{error}</div>
    </Modal>
  );
}
