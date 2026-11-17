// 统一迁移弹窗：scope=instance 迁移一台不可用实例，scope=account 迁移整个账号。
import { useState } from "react";
import Modal from "./Modal";
import { migrate } from "../api";

export default function MigrateModal({ tokens, instances, initialScope = "account", initialInstance = null, onClose, onDone }) {
  const [scope, setScope] = useState(initialScope);
  const [instanceId, setInstanceId] = useState(initialInstance ? String(initialInstance.id) : "");
  const [sourceId, setSourceId] = useState(
    initialInstance && initialInstance.token_id
      ? String(initialInstance.token_id)
      : tokens && tokens.length > 1
        ? String(tokens[0].id)
        : ""
  );
  const [targetId, setTargetId] = useState(tokens && tokens.length > 1 ? String(tokens[1].id) : "");
  const [deleteSource, setDeleteSource] = useState(false);
  const [rootPass, setRootPass] = useState("");
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState(null);
  const [result, setResult] = useState(null);

  async function run() {
    if (!targetId || targetId === sourceId) {
      setStatus({ msg: "请选择与目标不同的账号", cls: "error" });
      return;
    }
    if (scope === "instance" && !instanceId) {
      setStatus({ msg: "请选择要迁移的实例", cls: "error" });
      return;
    }
    if (scope === "account" && !sourceId) {
      setStatus({ msg: "请选择源账号", cls: "error" });
      return;
    }
    if (deleteSource && !window.confirm("迁移后将删除源账号上的实例，确认继续？")) return;
    setBusy(true);
    setStatus({ msg: "迁移中，可能需要几分钟…", cls: "" });
    setResult(null);
    try {
      const payload = { scope, target_token_id: parseInt(targetId, 10), delete_source: deleteSource };
      if (scope === "instance") {
        payload.instance_id = parseInt(instanceId, 10);
        if (sourceId) payload.source_token_id = parseInt(sourceId, 10);
        if (rootPass) payload.root_password = rootPass;
      } else {
        payload.source_token_id = parseInt(sourceId, 10);
      }
      const data = await migrate(payload);
      setResult(data);
      setStatus({
        msg: Array.isArray(data.results)
          ? `完成：成功 ${data.migrated}，失败 ${data.failed}`
          : "迁移完成",
        cls: data.failed ? "error" : "ok",
      });
      if (onDone) onDone();
    } catch (err) {
      setStatus({ msg: "迁移失败：" + (err.message || "未知错误"), cls: "error" });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal title="迁移" onClose={onClose}>
      <p className="boot-script-hint">
        把实例或整个账号的机器与面板配置（开机脚本、root 密码、自愈配置）迁移到目标账号：
        在目标账号按相同配置创建新实例并迁移缓存记录；可选删除源实例。
      </p>
      <div className="modal-field">
        <label>迁移范围</label>
        <select value={scope} onChange={(e) => setScope(e.target.value)} disabled={busy}>
          <option value="account">整个账号（全部实例与配置）</option>
          <option value="instance">单个实例（上游已不存在）</option>
        </select>
      </div>
      {scope === "account" && (
        <div className="modal-field">
          <label htmlFor="mig-source">源账号</label>
          <select id="mig-source" value={sourceId} onChange={(e) => setSourceId(e.target.value)} disabled={busy}>
            {(tokens || []).map((t) => (
              <option key={t.id} value={String(t.id)}>{t.label || "账号 #" + t.id}</option>
            ))}
          </select>
        </div>
      )}
      {scope === "instance" && (
        <>
          <div className="modal-field">
            <label htmlFor="mig-instance">实例</label>
            <select id="mig-instance" value={instanceId} onChange={(e) => setInstanceId(e.target.value)} disabled={busy}>
              <option value="">请选择实例</option>
              {(instances || []).filter((i) => i.missing).map((i) => (
                <option key={i.id} value={i.id}>#{i.id} {i.label || ""}</option>
              ))}
            </select>
          </div>
          <div className="modal-field">
            <label>实例所属账号（可选）</label>
            <select value={sourceId} onChange={(e) => setSourceId(e.target.value)} disabled={busy}>
              <option value="">自动判断</option>
              {(tokens || []).map((t) => (
                <option key={t.id} value={String(t.id)}>{t.label || "账号 #" + t.id}</option>
              ))}
            </select>
          </div>
          <div className="modal-field">
            <label>root 密码（实例未保存密码时必填）</label>
            <input type="password" value={rootPass} placeholder="留空表示沿用已保存的密码" onChange={(e) => setRootPass(e.target.value)} />
          </div>
        </>
      )}
      <div className="modal-field">
        <label htmlFor="mig-target">目标账号</label>
        <select id="mig-target" value={targetId} onChange={(e) => setTargetId(e.target.value)} disabled={busy}>
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
