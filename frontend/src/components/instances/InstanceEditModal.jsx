// 已迁移：单实例级配置已移除，编辑改为打开账号级配置（同账号所有实例共用）。
// 本组件仅作兼容转发：实例页打开时按该实例所属账号展示账号级配置。
import AccountConfigModal from "../accounts/AccountConfigModal";
import Modal from "../Modal";

export default function InstanceEditModal({
  inst,
  onClose,
  onScriptChanged,
  onAutoRebuildChanged,
}) {
  const tokenId = inst && inst.token_id != null ? inst.token_id : 0;
  if (!tokenId) {
    return (
      <Modal title={"编辑 · " + (inst.label || "#" + inst.id)} onClose={onClose}>
        <p className="boot-script-hint">
          该实例未归属账号，无法编辑账号级配置。请先绑定账号（PAT）。
        </p>
        <div className="modal-actions">
          <button className="btn btn-ghost" type="button" onClick={onClose}>
            关闭
          </button>
        </div>
      </Modal>
    );
  }
  const token = { id: tokenId, label: inst.token_label || "#" + tokenId };
  return (
    <AccountConfigModal
      token={token}
      onClose={onClose}
      onSaved={() => {
        if (onScriptChanged) onScriptChanged();
        if (onAutoRebuildChanged) onAutoRebuildChanged();
      }}
    />
  );
}
