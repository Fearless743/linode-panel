// 账号级「被墙自愈」统一配置弹窗：脚本、root 密码、检测参数与自愈模式
// （关闭 / 自动换 IP / 双机轮换）只有这一处，同账号所有实例共用。
import { useEffect, useState } from "react";
import Modal from "../Modal";
import { getSelfHeal, setSelfHeal, deleteSelfHeal, checkSelfHeal, rotateSelfHeal } from "../../api";

function fmtTime(s) {
  if (!s) return "—";
  const d = new Date(s);
  if (isNaN(d.getTime())) return s;
  return d.toLocaleString();
}

const MODES = [
  { value: "off", label: "关闭", hint: "不做被墙检测" },
  { value: "rebuild", label: "自动换 IP", hint: "被墙后删机重建，换一个公网 IP" },
  { value: "dual", label: "双机轮换", hint: "A 使用中 / B 备用，被墙时切到 B 并重建旧机" },
];

export default function AccountConfigModal({ token, onClose, onSaved, instances }) {
  const [mode, setMode] = useState("off");
  const [enabled, setEnabled] = useState(false);
  const [script, setScript] = useState("");
  const [clearScript, setClearScript] = useState(false);
  const [rootPass, setRootPass] = useState("");
  const [port, setPort] = useState("22");
  const [threshold, setThreshold] = useState("3");
  const [intervalMin, setIntervalMin] = useState("5");
  const [template, setTemplate] = useState("");
  const [hasPass, setHasPass] = useState(false);
  const [cfg, setCfg] = useState(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState(null);
  const list = Array.isArray(instances) ? instances : [];

  function applyConfig(data) {
    if (!data) return;
    setCfg(data);
    const m = data.mode === "rebuild" || data.mode === "dual" ? data.mode : "off";
    setMode(m);
    setEnabled(!!data.enabled);
    setScript(data.script || "");
    setPort(String(data.port || 22));
    setThreshold(String(data.threshold || 3));
    setIntervalMin(String(Math.round((data.interval_seconds || 300) / 60) || 5));
    setHasPass(!!data.has_root_password);
    if (data.active_instance_id) setTemplate(String(data.active_instance_id));
  }

  useEffect(() => {
    let alive = true;
    getSelfHeal(token.id)
      .then((data) => {
        if (alive) applyConfig(data);
      })
      .catch(() => {})
      .finally(() => {
        if (alive) setLoading(false);
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
    if (mode === "dual" && enabled && !template) {
      setStatus({ msg: "开启双机轮换时请先选择使用中实例（模板）", cls: "error" });
      return;
    }
    setBusy(true);
    setStatus({ msg: "保存中…", cls: "" });
    try {
      const payload = {
        mode,
        enabled,
        port: p,
        threshold: t,
        interval_seconds: m * 60,
        script: clearScript ? "" : script,
      };
      if (template && mode === "dual") payload.template_instance_id = parseInt(template, 10);
      if (rootPass) payload.root_password = rootPass;
      const data = await setSelfHeal(token.id, payload);
      applyConfig(data.self_heal || data);
      setHasPass(!!(data.self_heal || data).has_root_password);
      setStatus({
        msg: data.warning ? "已保存，但备用机创建失败：" + data.warning : "已保存（同账号所有实例共用）",
        cls: data.warning ? "error" : "ok",
      });
      if (onSaved) onSaved();
    } catch (err) {
      setStatus({ msg: "保存失败：" + (err.message || "未知错误"), cls: "error" });
    } finally {
      setBusy(false);
      setRootPass("");
    }
  }

  async function disable() {
    if (!window.confirm("关闭该账号的自愈监测？实例与配置会保留。")) return;
    setBusy(true);
    try {
      await deleteSelfHeal(token.id);
      applyConfig(await getSelfHeal(token.id));
      setStatus({ msg: "已关闭自愈", cls: "ok" });
      if (onSaved) onSaved();
    } catch (err) {
      setStatus({ msg: "关闭失败：" + (err.message || "未知错误"), cls: "error" });
    } finally {
      setBusy(false);
    }
  }

  async function checkNow() {
    setBusy(true);
    setStatus({ msg: "检测中…", cls: "" });
    try {
      applyConfig(await checkSelfHeal(token.id));
      setStatus({ msg: "检测完成", cls: "ok" });
      if (onSaved) onSaved();
    } catch (err) {
      setStatus({ msg: "检测失败：" + (err.message || "未知错误"), cls: "error" });
    } finally {
      setBusy(false);
    }
  }

  async function rotateNow() {
    if (!window.confirm("立即把使用中实例切换到备用机并重建旧机？")) return;
    setBusy(true);
    setStatus({ msg: "已触发立即替换，后台执行中…", cls: "" });
    try {
      await rotateSelfHeal(token.id);
      setTimeout(async () => {
        try {
          applyConfig(await getSelfHeal(token.id));
        } catch {}
      }, 2000);
    } catch (err) {
      setStatus({ msg: "触发失败：" + (err.message || "未知错误"), cls: "error" });
    } finally {
      setBusy(false);
    }
  }

  const active = mode !== "off" && enabled;

  return (
    <Modal title={"自愈设置 · " + (token.label || "#" + token.id)} onClose={onClose}>
      <p className="boot-script-hint">
        被墙检测只有一套配置：开机脚本、root 密码、检测端口与失败阈值在此统一设置；
        选择一种模式即可，模式之间互斥。同账号所有实例共用。
      </p>
      {loading ? (
        <div className="users-empty">正在加载…</div>
      ) : (
        <>
          <div className="modal-field">
            <label>自愈模式</label>
            <select value={mode} onChange={(e) => setMode(e.target.value)}>
              {MODES.map((m) => (
                <option key={m.value} value={m.value}>
                  {m.label} — {m.hint}
                </option>
              ))}
            </select>
          </div>

          {mode !== "off" && (
            <>
              <div className="modal-field row">
                <label className="switch-line">
                  <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
                  <span>启用自愈监测</span>
                </label>
              </div>

              <div className="modal-field">
                <label>开机脚本</label>
                <textarea rows={6} value={script} onChange={(e) => { setScript(e.target.value); setClearScript(false); }} />
                <label className="switch-line">
                  <input type="checkbox" checked={clearScript} onChange={(e) => setClearScript(e.target.checked)} />
                  <span>清除已保存的脚本</span>
                </label>
              </div>

              <div className="modal-field">
                <label>root 密码（用于 SSH 执行开机脚本 / 重建，不回显明文）</label>
                <input type="password" value={rootPass} placeholder={hasPass ? "已保存，留空表示不修改" : "请输入 root 密码"} onChange={(e) => setRootPass(e.target.value)} />
              </div>

              <div className="modal-grid">
                <div className="modal-field">
                  <label>检测端口（TCP）</label>
                  <input type="number" min="1" max="65535" value={port} onChange={(e) => setPort(e.target.value)} />
                </div>
                <div className="modal-field">
                  <label>连续失败次数</label>
                  <input type="number" min="1" max="20" value={threshold} onChange={(e) => setThreshold(e.target.value)} />
                </div>
                <div className="modal-field">
                  <label>检测间隔（分钟）</label>
                  <input type="number" min="1" max="1440" value={intervalMin} onChange={(e) => setIntervalMin(e.target.value)} />
                </div>
              </div>
              <p className="boot-script-hint">
                检测端口指向国内连通性探测端口（如 443、8443），与 SSH 登录端口互不影响。
              </p>

              {mode === "dual" && (
                <div className="modal-field">
                  <label>使用中实例（模板，作为 A）</label>
                  <select value={template} onChange={(e) => setTemplate(e.target.value)}>
                    <option value="">请选择实例</option>
                    {list.map((i) => (
                      <option key={i.id} value={i.id}>
                        #{i.id} {i.label || ""} {i.ipv4?.[0] ? "· " + i.ipv4[0] : ""}
                      </option>
                    ))}
                  </select>
                  <p className="boot-script-hint">
                    一个账号只管理一对 A/B；双机接管的实例会自动跳过单实例换 IP，避免重复删机。
                  </p>
                </div>
              )}

              {cfg && active && (
                <div className="modal-detail auto-rebuild-state">
                  <div>当前状态：{cfg.last_status || cfg.state || "—"}</div>
                  <div>最近检测：{fmtTime(cfg.last_check_at)}</div>
                  <div>详情：{cfg.last_detail || "—"}</div>
                  {mode === "dual" && (
                    <div>使用中 #{cfg.active_instance_id || "—"} · 备用 #{cfg.standby_instance_id || "—"}</div>
                  )}
                  {mode === "rebuild" && Array.isArray(cfg.instances) && (
                    <div style={{ maxHeight: 160, overflow: "auto" }}>
                      {cfg.instances.map((it) => (
                        <div key={it.instance_id}>
                          #{it.instance_id} · {it.last_status || "idle"} · 失败 {it.fail_count}
                        </div>
                      ))}
                    </div>
                  )}
                </div>
              )}

              <div className="actions" style={{ marginTop: 8 }}>
                <button className="btn btn-primary" type="button" disabled={busy} onClick={save}>
                  {busy ? "处理中…" : "保存"}
                </button>
                {active && (
                  <button className="btn btn-ghost" type="button" disabled={busy} onClick={checkNow}>
                    立即检测
                  </button>
                )}
                {active && mode === "dual" && (
                  <button className="btn btn-ghost" type="button" disabled={busy} onClick={rotateNow}>
                    立即替换
                  </button>
                )}
                <button className="btn btn-danger" type="button" disabled={busy} onClick={disable}>
                  关闭自愈
                </button>
              </div>
            </>
          )}

          {status && <div className={"boot-script-status " + status.cls} style={{ marginTop: 8 }}>{status.msg}</div>}
        </>
      )}
    </Modal>
  );
}
