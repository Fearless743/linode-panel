// 账号迁移：把一个账号的全部实例与面板配置迁移到另一个账号。
import { useState } from "react";
import Modal from "../Modal";
import { migrateAccount } from "../../api";

export default function AccountMigrateModal({ tokens, onClose }) {
  const [sourceId, setSourceId] = useState(tokens && tokens.length > 1 ? String(tokens[0].id) : "");
  const [targetId, setTargetId] = useState(tokens && tokens.length > 1 ? String(tokens[1].id) : "");
  const [deleteSource, setDeleteSource] = useState(false);
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState(null);
  const [result, setResult] = useState(null);

  async function run() {
    if (!sourceId || !targetId || sourceId === targetId) {
      setStatus({ msg: "请选择不同的源账号与目标账号", cls: "error" });
      return;
    }
    if (deleteSource && !window.confirm("迁移后将删除源账号上的实例，确认继续？")) return;
    setBusy(true);
    setStatus({ msg: "迁移中，可能需要几分钟…", cls: "" });
    setResult(null);
    try {
      const data = await migrateAccount({
        source_token_id: parseInt(sourceId, 10),
        target_token_id: parseInt(targetId, 10),
        delete_source: deleteSource,
      });
      setResult(data);
      setStatus({ msg: `完成：成功 ${data.migrated}，失败 ${data.failed}`, cls: data.failed ? "error" : "ok" });
    } catch (err) {
      setStatus({ msg: "迁移失败：" + (err.message || "未知错误"), cls: "error" });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal title="账号迁移" onClose={onClose}>
      <p className="boot-script-hint">
        把源账号的全部实例与面板配置（开机脚本、root 密码、自动换 IP、替换配置）迁移到目标账号：
        在目标账号按相同配置创建新实例并迁移缓存记录；可选删除源实例。账号级双机配置与 Agent 绑定也会同步。
      </p>
      <div className="modal-field">
        <label htmlFor="mig-source">源账号</label>
        <select id="mig-source" className="modal-input" value={sourceId} onChange={(e) => setSourceId(e.target.value)} disabled={busy}>
          {(tokens || []).map((t) => (
            <option key={t.id} value={String(t.id)}>{t.label || "账号 #" + t.id}</option>
          ))}
        </select>
      </div>
      <div className="modal-field">
        <label htmlFor="mig-target">目标账号</label>
        <select id="mig-target" className="modal-input" value={targetId} onChange={(e) => setTargetId(e.target.value)} disabled={busy}>
          {(tokens || []).map((t) => (
            <option key={t.id} value={String(t.id)}>{t.label || "账号 #" + t.id}</option>
          ))}
        </select>
      </div>
      <div className="modal-field">
        <label className="exec-option">
          <input type="checkbox" checked={deleteSource} onChange={(e) => setDeleteSource(e.target.checked)} disabled={busy} />
          <span className="exec-option-text">
            <span>迁移后删除源实例</span>
            <span className="exec-option-sub">关闭则源实例保留（仅复制配置）</span>
          </span>
        </label>
      </div>
      {status && <div className={"boot-script-status " + status.cls}>{status.msg}</div>}
      {result && Array.isArray(result.results) && (
        <div className="modal-detail auto-rebuild-state" style={{ maxHeight: 200, overflow: "auto" }}>
          {result.results.map((r, i) => (
            <div key={i}>
              #{r.source_instance_id} → {r.ok ? `#${r.target_instance_id} OK` : `失败：${r.error}`}
            </div>
          ))}
        </div>
      )}
      <div className="modal-actions">
        <button className="btn btn-ghost" type="button" onClick={onClose} disabled={busy}>关闭</button>
        <button className="btn btn-primary" type="button" onClick={run} disabled={busy}>
          {busy ? "迁移中…" : "开始迁移"}
        </button>
      </div>
    </Modal>
  );
}
