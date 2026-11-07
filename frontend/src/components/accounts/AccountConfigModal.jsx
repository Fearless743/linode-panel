// 账号级配置弹窗：开机脚本、root 密码、自动换 IP 全在账号里，
// 同账号所有实例共用；实例表仅为缓存。
import { useEffect, useState } from "react";
import Modal from "../Modal";
import { getAccountConfig, setAccountConfig } from "../../api";

export default function AccountConfigModal({ token, onClose, onSaved }) {
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

  useEffect(() => {
    let alive = true;
    getAccountConfig(token.id)
      .then((d) => {
        if (!alive) return;
        setScript(d.script || "");
        setEnabled(!!d.auto_enabled);
        setPort(String(d.auto_port || 22));
        setThreshold(String(d.auto_threshold || 3));
        setIntervalMin(String(Math.round((d.auto_interval_seconds || 300) / 60) || 5));
        setHasPass(!!d.has_root_password);
        setLoading(false);
      })
      .catch(() => {
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
    if (!clearScript && !script.trim()) {
      setStatus({ msg: "开机脚本不能为空（或勾选清除）", cls: "error" });
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
      setStatus({ msg: "已保存（同账号所有实例共用）", cls: "ok" });
      if (onSaved) onSaved();
    } catch (err) {
      setStatus({ msg: "保存失败：" + (err.message || "未知错误"), cls: "error" });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal title={"账号配置 · " + (token.label || "#" + token.id)} onClose={onClose}>
      <p className="boot-script-hint">
        实例配置已统一为账号级：同账号下所有实例共用开机会发脚本、root 密码与自动换 IP
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
