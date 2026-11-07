// 账号级配置弹窗：开机脚本、root 密码、自动换 IP、双机轮询开关共处一处，
// 同账号所有实例共用；实例表仅为缓存。
import { useEffect, useState } from "react";
import Modal from "../Modal";
import { getAccountConfig, setAccountConfig, listDual, checkDual, forceDualRotate, setDual } from "../../api";

function fmtTime(s) {
  if (!s) return "—";
  const d = new Date(s);
  if (isNaN(d.getTime())) return s;
  return d.toLocaleString();
}

export default function AccountConfigModal({ token, onClose, onSaved, instances }) {
  const [script, setScript] = useState("");
  const [clearScript, setClearScript] = useState(false);
  const [rootPass, setRootPass] = useState("");
  const [clearPass, setClearPass] = useState(false);
  const [enabled, setEnabled] = useState(false);
  const [port, setPort] = useState("22");
  const [threshold, setThreshold] = useState("3");
  const [intervalMin, setIntervalMin] = useState("5");
  const [hasPass, setHasPass] = useState(false);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState(null);

  // dual (switch)
  const [dualEnabled, setDualEnabled] = useState(false);
  const [dualTemplate, setDualTemplate] = useState("");
  const [dual, setDual] = useState(null);
  const [rotating, setRotating] = useState(false);
  const list = Array.isArray(instances) ? instances : [];

  useEffect(() => {
    let alive = true;
    Promise.all([
      getAccountConfig(token.id).catch(() => null),
      listDual().catch(() => ({ dual: [] })),
    ]).then(([ac, dl]) => {
      if (!alive) return;
      if (ac) {
        setScript(ac.script || "");
        setEnabled(!!ac.auto_enabled);
        setPort(String(ac.auto_port || ac.dual_port || 22));
        setThreshold(String(ac.auto_threshold || ac.dual_threshold || 3));
        setIntervalMin(String(Math.round((ac.auto_interval_seconds || ac.dual_interval_seconds || 300) / 60) || 5));
        setHasPass(!!ac.has_root_password);
        setDualEnabled(!!ac.dual_enabled);
        setDualTemplate(ac.dual_active_instance_id ? String(ac.dual_active_instance_id) : "");
      }
      const dualList = dl && Array.isArray(dl.dual) ? dl.dual : [];
      const found = dualList.find((d) => d.token_id === token.id) || null;
      if (found) {
        setDual(found);
        setDualEnabled(!!found.enabled);
        setDualTemplate(found.active_instance_id ? String(found.active_instance_id) : "");
      }
      setLoading(false);
    });
    return () => {
      alive = false;
    };
  }, [token.id]);

  async function save() {
    const p = parseInt(port, 10);
    const t = parseInt(threshold, 10);
    const m = parseInt(intervalMin, 10);
    if (!(p >= 1 && p <= 65535)) {
      setStatus({ msg: "检测端口必须在 1-65535 之间", cls: "error" });
      return;
    }
    if (!(t >= 1 && t <= 20)) {
      setStatus({ msg: "连续失败次数必须在 1-20 之间", cls: "error" });
      return;
    }
    if (!(m >= 1 && m <= 1440)) {
      setStatus({ msg: "检测间隔必须在 1-1440 分钟之间", cls: "error" });
      return;
    }
    if (!clearScript && !script.trim()) {
      setStatus({ msg: "开机脚本不能为空（或勾选清除）", cls: "error" });
      return;
    }
    if (dualEnabled && !dual?.active_instance_id && !dualTemplate) {
      setStatus({ msg: "开启双机时请先选择模板实例（模板机将作为使用中 A）", cls: "error" });
      return;
    }
    setBusy(true);
    setStatus({ msg: "保存中…", cls: "" });
    try {
      const payload = {
        script: clearScript ? "" : script,
        clear_script: clearScript,
        clear_root_password: clearPass,
        enabled,
        port: p,
        threshold: t,
        interval_seconds: m * 60,
      };
      if (rootPass) payload.root_password = rootPass;
      const d = await setAccountConfig(token.id, payload);
      setHasPass(!!d.has_root_password);
      // dual switch is separate endpoint but reuses the same script/root/port params
      const dualPayload = {
        enabled: dualEnabled,
        port: p,
        threshold: t,
        interval_seconds: m * 60,
        script: clearScript ? "" : script,
      };
      if (dualTemplate) dualPayload.template_instance_id = parseInt(dualTemplate, 10);
      if (rootPass) dualPayload.root_password = rootPass;
      let dualData = null;
      try {
        dualData = await setDual(token.id, dualPayload);
      } catch (err) {
        if (dualEnabled) throw err;
      }
      if (dualData?.dual) setDual(dualData.dual);
      if (dualData?.warning) {
        setStatus({ msg: "账号已保存，双机备用机创建失败：" + dualData.warning, cls: "error" });
      } else {
        setStatus({ msg: "已保存（同账号所有实例共用）", cls: "ok" });
      }
      if (onSaved) onSaved();
    } catch (err) {
      setStatus({ msg: "保存失败：" + (err.message || "未知错误"), cls: "error" });
    } finally {
      setBusy(false);
    }
  }

  async function checkNow() {
    setBusy(true);
    setStatus({ msg: "检测中…", cls: "" });
    try {
      const data = await checkDual(token.id);
      if (data && data.dual) setDual(data.dual);
      setStatus({ msg: "检测完成", cls: "ok" });
      if (onSaved) onSaved();
    } catch (err) {
      setStatus({ msg: "检测失败：" + (err.message || "未知错误"), cls: "error" });
    } finally {
      setBusy(false);
    }
  }

  async function rotateNow() {
    if (!dual?.active_instance_id) return;
    if (!window.confirm("立即把使用中实例切换到备用机并重建旧机？")) return;
    setRotating(true);
    setStatus({ msg: "已触发立即替换，后台执行中…", cls: "" });
    try {
      await forceDualRotate(token.id);
      setTimeout(async () => {
        try {
          const data = await checkDual(token.id);
          if (data && data.dual) setDual(data.dual);
        } catch {}
      }, 2000);
    } catch (err) {
      setStatus({ msg: "触发失败：" + (err.message || "未知错误"), cls: "error" });
    } finally {
      setRotating(false);
    }
  }

  return (
    <Modal title={"账号配置 · " + (token.label || "#" + token.id)} onClose={onClose}>
      <p className="boot-script-hint">
        配置只在账号一处：开机脚本、root 密码、自动换 IP 与双机轮询共用，同账号所有实例一致，实例表仅为缓存。
      </p>
      {loading ? (
        <div>加载中…</div>
      ) : (
        <>
          <div className="modal-field">
            <label htmlFor="acct-script">开机脚本（账号级）</label>
            <textarea
              id="acct-script"
              className="boot-script-textarea"
              rows={6}
              spellCheck={false}
              value={script}
              onChange={(e) => setScript(e.target.value)}
              disabled={busy}
            />
            <label className="exec-option">
              <input
                type="checkbox"
                checked={clearScript}
                onChange={(e) => setClearScript(e.target.checked)}
                disabled={busy}
              />
              <span className="exec-option-text">
                <span>清除脚本（回退默认）</span>
              </span>
            </label>
          </div>
          <div className="modal-field">
            <label htmlFor="acct-pass">
              root 密码{hasPass ? "（已保存，留空不改）" : "（重建/创建用）"}
            </label>
            <input
              id="acct-pass"
              className="modal-input"
              type="password"
              autoComplete="new-password"
              placeholder={hasPass ? "留空保持不变" : "输入 root 密码"}
              value={rootPass}
              onChange={(e) => setRootPass(e.target.value)}
              disabled={busy || clearPass}
            />
            {hasPass && (
              <label className="exec-option">
                <input
                  type="checkbox"
                  checked={clearPass}
                  onChange={(e) => setClearPass(e.target.checked)}
                  disabled={busy}
                />
                <span className="exec-option-text">
                  <span>清除已保存密码</span>
                </span>
              </label>
            )}
          </div>
          <div className="modal-field">
            <label className="exec-option">
              <input
                type="checkbox"
                checked={enabled}
                onChange={(e) => setEnabled(e.target.checked)}
                disabled={busy}
              />
              <span className="exec-option-text">
                <span>开启自动换 IP（账号级）</span>
                <span className="exec-option-sub">该账号下所有实例按同一参数被墙后重建</span>
              </span>
            </label>
          </div>
          <div className="modal-field">
            <label htmlFor="acct-port">检测端口（TCP）</label>
            <input
              id="acct-port"
              className="modal-input"
              type="number"
              min="1"
              max="65535"
              value={port}
              onChange={(e) => setPort(e.target.value)}
              disabled={busy}
            />
          </div>
          <div className="modal-field">
            <label htmlFor="acct-th">连续失败多少次判定被墙</label>
            <input
              id="acct-th"
              className="modal-input"
              type="number"
              min="1"
              max="20"
              value={threshold}
              onChange={(e) => setThreshold(e.target.value)}
              disabled={busy}
            />
          </div>
          <div className="modal-field">
            <label htmlFor="acct-iv">检测间隔（分钟）</label>
            <input
              id="acct-iv"
              className="modal-input"
              type="number"
              min="1"
              max="1440"
              value={intervalMin}
              onChange={(e) => setIntervalMin(e.target.value)}
              disabled={busy}
            />
          </div>

          <div className="modal-field">
            <h3 style={{ margin: "12px 0 6px" }}>双机轮询</h3>
            <label className="exec-option">
              <input
                type="checkbox"
                checked={dualEnabled}
                onChange={(e) => setDualEnabled(e.target.checked)}
                disabled={busy}
              />
              <span className="exec-option-text">
                <span>开启双机轮询</span>
                <span className="exec-option-sub">一个账号只管理一对 A/B；双机实例自动跳过自动换IP</span>
              </span>
            </label>
          </div>
          {dualEnabled && (
            <>
              <div className="modal-field">
                <label htmlFor="dual-template">模板实例（作为使用中 A）</label>
                <select
                  id="dual-template"
                  className="modal-input"
                  value={dualTemplate}
                  onChange={(e) => setDualTemplate(e.target.value)}
                  disabled={busy}
                  title="可重新选择：切换后旧主成为备用机并触发重建（若已被删则新建）"
                >
                  <option value="">选择该账号下的实例…</option>
                  {list.map((i) => (
                    <option key={i.id} value={String(i.id)}>
                      {i.label || "未命名"} #{i.id}（{i.status}）
                    </option>
                  ))}
                </select>
                {dual && dual.active_instance_id > 0 && (
                  <p className="boot-script-hint">
                    使用中：#{dual.active_instance_id}
                    {dual.standby_instance_id > 0 ? ` · 备用：#${dual.standby_instance_id}` : " · 备用机创建中…"}
                  </p>
                )}
                {list.length === 0 && (
                  <p className="boot-script-hint">
                    该账号下暂无可用实例，请先到实例页创建一台后再回来选择作为使用中。双机实例被删后仅记录了指针，上游已无实例时必须先有新实例才能重绑。
                  </p>
                )}
              </div>
              {dual && (
                <div className="modal-detail auto-rebuild-state">
                  <div>状态：{dual.state || "—"} · {dual.last_status || "—"}</div>
                  <div>连续失败：{dual.fail_count || 0}</div>
                  <div>最近检测：{fmtTime(dual.last_check_at)}</div>
                  {dual.last_detail ? <div>详情：{dual.last_detail}</div> : null}
                </div>
              )}
              <div className="modal-actions">
                <button
                  className="btn btn-ghost"
                  type="button"
                  onClick={checkNow}
                  disabled={busy || !dual?.active_instance_id}
                >
                  立即检测
                </button>
                <button
                  className="btn btn-danger"
                  type="button"
                  onClick={rotateNow}
                  disabled={busy || rotating || !dual?.active_instance_id}
                >
                  {rotating ? "触发中…" : "立即替换"}
                </button>
              </div>
            </>
          )}
        </>
      )}
      {status && <div className={"boot-script-status " + status.cls}>{status.msg}</div>}
      <div className="modal-actions">
        <button className="btn btn-ghost" type="button" onClick={onClose} disabled={busy}>
          关闭
        </button>
        <button className="btn btn-primary" type="button" onClick={save} disabled={busy || loading}>
          保存
        </button>
      </div>
    </Modal>
  );
}
