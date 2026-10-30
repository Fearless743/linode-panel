import { useMemo, useState } from "react";
import Modal from "../Modal";
import { deleteInstanceFailover, setInstanceFailover } from "../../api";
import { describeFailover } from "../../utils/accountGroups";

// 与 AccountGroupCard 一致：failover 色调 -> status-pill 样式类。
const TONE_PILL = {
  ok: "status-running",
  warn: "status-transition",
  danger: "status-danger",
  muted: "status-unknown",
};

// ---------- 成员账号自动替换配置模态框 ----------
// 后端「替换」按实例（instance_id）配置。这里先列出该成员账号下的全部实例
// （GET /api/instances 的 instances[].token_id 已确认存在），再对每个实例提供
// 开关 + 失败阈值 + 检查间隔，保存时调用 setInstanceFailover 写入
// token_id / group_id / enabled / threshold / interval_seconds。
export default function FailoverConfigModal({
  group,
  member,
  instances,
  failovers,
  onClose,
  onChanged,
}) {
  const tokenInstances = useMemo(() => {
    const list = Array.isArray(instances) ? instances : [];
    return list.filter((i) => i && String(i.token_id) === String(member.id));
  }, [instances, member.id]);

  const failoverByInstance = useMemo(() => {
    const map = new Map();
    for (const f of Array.isArray(failovers) ? failovers : []) {
      if (f && f.instance_id != null) map.set(String(f.instance_id), f);
    }
    return map;
  }, [failovers]);

  const [forms, setForms] = useState(() => {
    const next = {};
    for (const inst of tokenInstances) {
      const cfg = failoverByInstance.get(String(inst.id));
      next[String(inst.id)] = {
        enabled: !!(cfg && cfg.enabled),
        threshold: String((cfg && cfg.threshold) || 3),
        interval: String((cfg && cfg.interval_seconds) || 300),
      };
    }
    return next;
  });
  const [busyId, setBusyId] = useState(null);
  const [errors, setErrors] = useState({});
  const [notice, setNotice] = useState("");

  function updateForm(inst, patch) {
    const key = String(inst.id);
    setForms((prev) => ({
      ...prev,
      [key]: { enabled: false, threshold: "3", interval: "300", ...prev[key], ...patch },
    }));
  }

  function setRowError(key, msg) {
    setErrors((prev) => ({ ...prev, [key]: msg }));
  }

  function clearRowError(key) {
    setErrors((prev) => {
      if (!prev[key]) return prev;
      const next = { ...prev };
      delete next[key];
      return next;
    });
  }

  async function save(inst) {
    const key = String(inst.id);
    const form = forms[key] || { enabled: false, threshold: "3", interval: "300" };
    const threshold = parseInt(form.threshold, 10);
    const interval = parseInt(form.interval, 10);
    if (!(threshold >= 1 && threshold <= 20)) {
      setRowError(key, "失败阈值需在 1-20 之间");
      return;
    }
    if (!(interval >= 10 && interval <= 86400)) {
      setRowError(key, "检查间隔需在 10-86400 秒之间");
      return;
    }
    setBusyId(key);
    clearRowError(key);
    setNotice("");
    try {
      await setInstanceFailover(inst.id, {
        token_id: member.id,
        group_id: group.id,
        enabled: !!form.enabled,
        threshold,
        interval_seconds: interval,
      });
      setNotice("已保存：" + (inst.label || "#" + inst.id));
      if (onChanged) onChanged();
    } catch (err) {
      setRowError(key, (err && err.message) || "保存失败");
    } finally {
      setBusyId(null);
    }
  }

  async function remove(inst) {
    const key = String(inst.id);
    setBusyId(key);
    clearRowError(key);
    setNotice("");
    try {
      await deleteInstanceFailover(inst.id);
      setNotice("已移除配置：" + (inst.label || "#" + inst.id));
      if (onChanged) onChanged();
    } catch (err) {
      setRowError(key, (err && err.message) || "移除失败");
    } finally {
      setBusyId(null);
    }
  }

  const anyBusy = busyId !== null;

  return (
    <Modal
      title={"自动替换 · " + (member.label || "#" + member.id)}
      onClose={onClose}
    >
      <p className="modal-detail">
        自动替换按实例配置：实例所在账号连续失败达到阈值后，面板会在账号组「
        {group.name || "#" + group.id}」内换另一个账号，用相同配置重建替换。开关与参数
        保存后立即生效。
      </p>

      {tokenInstances.length === 0 ? (
        <div className="users-empty">
          该账号下没有实例。自动替换按实例配置，请先在该账号下创建实例。
        </div>
      ) : (
        tokenInstances.map((inst) => {
          const key = String(inst.id);
          const form =
            forms[key] || { enabled: false, threshold: "3", interval: "300" };
          const cfg = failoverByInstance.get(key);
          const status = describeFailover(cfg);
          const busy = busyId === key;
          return (
            <div className="ag-failover-item" key={inst.id}>
              <div className="ag-failover-head">
                <div className="ag-failover-title">
                  {inst.label || "（未命名）"}
                  <span className="ag-member-id">#{inst.id}</span>
                </div>
                <span
                  className={"status-pill " + (TONE_PILL[status.tone] || "status-unknown")}
                >
                  <span>{status.label}</span>
                </span>
              </div>

              <label className="ag-failover-toggle">
                <input
                  type="checkbox"
                  checked={form.enabled}
                  disabled={busy}
                  onChange={(e) => updateForm(inst, { enabled: e.target.checked })}
                />
                启用自动替换
              </label>

              <div className="ag-failover-fields">
                <label className="ag-failover-field">
                  连续失败阈值（1-20）
                  <input
                    className="modal-input"
                    type="number"
                    min="1"
                    max="20"
                    value={form.threshold}
                    disabled={busy}
                    onChange={(e) => updateForm(inst, { threshold: e.target.value })}
                  />
                </label>
                <label className="ag-failover-field">
                  检查间隔（秒，10-86400）
                  <input
                    className="modal-input"
                    type="number"
                    min="10"
                    max="86400"
                    value={form.interval}
                    disabled={busy}
                    onChange={(e) => updateForm(inst, { interval: e.target.value })}
                  />
                </label>
              </div>

              {cfg && (
                <div className="ag-failover-meta">
                  连续失败 {cfg.fail_count || 0} 次
                  {cfg.last_detail ? " · " + cfg.last_detail : ""}
                  {cfg.replaced_by_instance_id
                    ? " · 已替换为 #" + cfg.replaced_by_instance_id
                    : ""}
                </div>
              )}
              {errors[key] && <div className="ag-failover-error">{errors[key]}</div>}

              <div className="ag-failover-actions">
                <button
                  className="btn btn-primary btn-sm"
                  type="button"
                  onClick={() => save(inst)}
                  disabled={busy}
                >
                  {busy ? "处理中…" : "保存"}
                </button>
                {cfg && (
                  <button
                    className="btn btn-danger btn-sm"
                    type="button"
                    onClick={() => remove(inst)}
                    disabled={busy}
                  >
                    移除配置
                  </button>
                )}
              </div>
            </div>
          );
        })
      )}

      {notice && <div className="boot-script-status ok">{notice}</div>}

      <div className="modal-actions">
        <button className="btn btn-ghost" type="button" onClick={onClose} disabled={anyBusy}>
          关闭
        </button>
      </div>
    </Modal>
  );
}
