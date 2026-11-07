// 双机轮询设置弹窗：账号级开关 + 模板实例 + 探测参数 + 脚本/密码。
import { useState } from "react";
import Modal from "../Modal";
import { setDual, checkDual, forceDualRotate } from "../../api";

function fmtTime(s) {
  if (!s) return "—";
  const d = new Date(s);
  if (isNaN(d.getTime())) return s;
  return d.toLocaleString();
}

export default function DualModal({ token, dual, instances, onClose, onSaved }) {
  const [enabled, setEnabled] = useState(dual ? !!dual.enabled : true);
  const [templateId, setTemplateId] = useState(
    dual && dual.active_instance_id ? String(dual.active_instance_id) : ""
  );
  const [port, setPort] = useState(dual && dual.port ? String(dual.port) : "22");
  const [threshold, setThreshold] = useState(
    dual && dual.threshold ? String(dual.threshold) : "3"
  );
  const [intervalMin, setIntervalMin] = useState(
    dual && dual.interval_seconds ? String(Math.round(dual.interval_seconds / 60)) : "5"
  );
  const [script, setScript] = useState(dual && dual.script ? dual.script : "");
  const [rootPass, setRootPass] = useState("");
  const [status, setStatus] = useState(null);
  const [busy, setBusy] = useState(false);
  const [detail, setDetail] = useState(dual || null);

  const [rotating, setRotating] = useState(false);

  async function rotateNow() {
    if (!detail || !detail.active_instance_id) return;
    if (!window.confirm("立即把使用中实例切换到备用机并重建旧机？")) return;
    setRotating(true);
    setStatus({ msg: "已触发立即替换，后台执行中…", cls: "" });
    try {
      await forceDualRotate(token.id);
      setTimeout(async () => {
        try {
          const data = await checkDual(token.id);
          if (data && data.dual) setDetail(data.dual);
        } catch {}
      }, 2000);
    } catch (err) {
      setStatus({ msg: "触发失败：" + (err.message || "未知错误"), cls: "error" });
    } finally {
      setRotating(false);
    }
  }

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
    if (enabled && !detail?.active_instance_id && !templateId) {
      setStatus({ msg: "请先选择模板实例（模板机将作为使用中 A）", cls: "error" });
      return;
    }
    setBusy(true);
    setStatus({ msg: "保存中…", cls: "" });
    try {
      const payload = {
        enabled,
        port: p,
        threshold: t,
        interval_seconds: m * 60,
        script,
      };
      if (templateId) payload.template_instance_id = parseInt(templateId, 10);
      if (rootPass) payload.root_password = rootPass;
      const data = await setDual(token.id, payload);
      if (data && data.warning) {
        setStatus({ msg: "配置已保存，但备用机创建失败：" + data.warning, cls: "error" });
      } else {
        setStatus({ msg: "已保存", cls: "ok" });
      }
      if (data && data.dual) setDetail(data.dual);
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
      if (data && data.dual) setDetail(data.dual);
      setStatus({ msg: "检测完成", cls: "ok" });
      if (onSaved) onSaved();
    } catch (err) {
      setStatus({ msg: "检测失败：" + (err.message || "未知错误"), cls: "error" });
    } finally {
      setBusy(false);
    }
  }

  const list = Array.isArray(instances) ? instances : [];

  return (
    <Modal title={"双机轮询 · " + (token.label || "#" + token.id)} onClose={onClose}>
      <p className="boot-script-hint">
        开启后该账号保持两台同配置实例：A 使用中（运行+执行脚本）、B 备用（关机）。
        被墙时自动关 A、开机 B 并执行脚本，再把旧 A 删机重建（不执行脚本）循环直到拿到未墙 IP。
      </p>
      <div className="modal-field">
        <label className="exec-option">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
            disabled={busy}
          />
          <span className="exec-option-text">
            <span>开启双机轮询</span>
            <span className="exec-option-sub">一个账号只管理一对 A/B；双机实例自动跳过自动换IP/账号组替换</span>
          </span>
        </label>
      </div>
      <div className="modal-field">
        <label htmlFor="dual-template">模板实例（作为使用中 A）</label>
        <select
          id="dual-template"
          className="modal-input"
          value={templateId}
          onChange={(e) => setTemplateId(e.target.value)}
          disabled={busy || (detail && detail.active_instance_id > 0)}
          title={detail && detail.active_instance_id > 0 ? "已确定 A，模板不可更改" : ""}
        >
          <option value="">选择该账号下的实例…</option>
          {list.map((i) => (
            <option key={i.id} value={String(i.id)}>
              {i.label || "未命名"} #{i.id}（{i.status}）
            </option>
          ))}
        </select>
        {detail && detail.active_instance_id > 0 && (
          <p className="boot-script-hint">
            使用中：#{detail.active_instance_id}
            {detail.standby_instance_id > 0 ? ` · 备用：#${detail.standby_instance_id}` : " · 备用机创建中…"}
          </p>
        )}
      </div>
      <div className="modal-field">
        <label htmlFor="dual-port">检测端口（TCP）</label>
        <input
          id="dual-port"
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
        <label htmlFor="dual-threshold">连续失败多少次判定被墙</label>
        <input
          id="dual-threshold"
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
        <label htmlFor="dual-interval">检测间隔（分钟）</label>
        <input
          id="dual-interval"
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
        <label htmlFor="dual-script">开机脚本（账号级，两台共用）</label>
        <textarea
          id="dual-script"
          className="boot-script-textarea"
          rows={6}
          spellCheck={false}
          placeholder={"#!/usr/bin/env bash\n# B 扶正时以 root 执行…"}
          value={script}
          onChange={(e) => setScript(e.target.value)}
          disabled={busy}
        />
      </div>
      <div className="modal-field">
        <label htmlFor="dual-rootpass">root 密码{detail?.has_root_password ? "（已保存，留空不改）" : "（必填）"}</label>
        <input
          id="dual-rootpass"
          className="modal-input"
          type="password"
          autoComplete="new-password"
          placeholder={detail?.has_root_password ? "留空保持不变" : "用于建机与 SSH 执行脚本"}
          value={rootPass}
          onChange={(e) => setRootPass(e.target.value)}
          disabled={busy}
        />
      </div>
      {detail && (
        <div className="modal-detail auto-rebuild-state">
          <div>状态：{detail.state || "—"} · {detail.last_status || "—"}</div>
          <div>连续失败：{detail.fail_count || 0}</div>
          <div>最近检测：{fmtTime(detail.last_check_at)}</div>
          {detail.last_detail ? <div>详情：{detail.last_detail}</div> : null}
        </div>
      )}
      {status && <div className={"boot-script-status " + status.cls}>{status.msg}</div>}
      <div className="modal-actions">
        <button className="btn btn-ghost" type="button" onClick={onClose} disabled={busy}>
          关闭
        </button>
        {detail && detail.active_instance_id > 0 && (
          <button className="btn btn-ghost" type="button" onClick={checkNow} disabled={busy}>
            立即检测
          </button>
        )}
        {detail && detail.active_instance_id > 0 && (
          <button className="btn btn-danger" type="button" onClick={rotateNow} disabled={busy || rotating}>
            {rotating ? "触发中…" : "立即替换"}
          </button>
        )}
        <button className="btn btn-primary" type="button" onClick={save} disabled={busy}>
          {busy ? "保存中…" : "保存"}
        </button>
      </div>
    </Modal>
  );
}
