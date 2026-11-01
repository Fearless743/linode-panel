// 手动迁移模态框：把上游已不存在（不可用）的实例用缓存配置在另一个账号上
// 重建，并把面板侧配置（脚本、root 密码、自动换 IP、替换配置）迁移过去。
import { useState } from "react";
import Modal from "../Modal";
import { Button } from "../ui";
import { regionLabel, typeLabel } from "../../utils/linodeLabels";

export default function MigrateInstanceModal({ inst, tokens, onClose, onConfirm }) {
  const candidates = (tokens || []).filter((t) => t.id !== inst.token_id);
  const [targetToken, setTargetToken] = useState(
    candidates.length > 0 ? String(candidates[0].id) : ""
  );
  const [rootPassword, setRootPassword] = useState("");
  const [label, setLabel] = useState(inst.label || "");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function confirm() {
    setError("");
    if (!targetToken) {
      setError("请选择目标账号");
      return;
    }
    setBusy(true);
    try {
      await onConfirm({
        targetTokenId: parseInt(targetToken, 10),
        rootPassword,
        label: label.trim(),
      });
      onClose();
    } catch (err) {
      setError(err && err.message ? err.message : "迁移失败");
      setBusy(false);
    }
  }

  return (
    <Modal title="迁移实例到其他账号" onClose={onClose}>
      <p className="modal-detail">
        实例「{inst.label || "#" + inst.id}」在 Linode 已不存在。将使用缓存中的
        {inst.region ? " " + regionLabel(inst.region) : ""}
        {inst.type ? " / " + typeLabel(inst.type) : ""}
        {" 配置在目标账号上创建一个新实例，并把面板配置迁移过去。"}
      </p>
      <p className="modal-detail">来源账号：{inst.token_label || "未绑定账号"}</p>
      <div className="modal-field">
        <label htmlFor="migrate-target">目标账号</label>
        <select
          id="migrate-target"
          className="modal-input"
          value={targetToken}
          onChange={(e) => setTargetToken(e.target.value)}
          disabled={busy}
        >
          {candidates.length === 0 && <option value="">（没有其他可用账号）</option>}
          {candidates.map((t) => (
            <option key={t.id} value={String(t.id)}>
              {t.label || "账号 #" + t.id}
            </option>
          ))}
        </select>
      </div>
      <div className="modal-field">
        <label htmlFor="migrate-label">实例名称</label>
        <input
          id="migrate-label"
          className="modal-input"
          type="text"
          value={label}
          onChange={(e) => setLabel(e.target.value)}
          disabled={busy}
        />
      </div>
      <div className="modal-field">
        <label htmlFor="migrate-rootpass">root 密码</label>
        <input
          id="migrate-rootpass"
          className="modal-input"
          type="password"
          value={rootPassword}
          onChange={(e) => setRootPassword(e.target.value)}
          placeholder="未保存 root 密码时必须填写"
          autoComplete="new-password"
          disabled={busy}
        />
      </div>
      <div className="modal-actions">
        <Button variant="ghost" onClick={onClose} disabled={busy}>
          取消
        </Button>
        <Button variant="primary" onClick={confirm} disabled={busy || !targetToken}>
          {busy ? "迁移中…" : "确认迁移"}
        </Button>
      </div>
      <div className="modal-error">{error}</div>
    </Modal>
  );
}
