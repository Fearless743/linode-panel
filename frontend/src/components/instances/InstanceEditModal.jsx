// 实例「编辑」模态框：合并开机脚本与自动换 IP 两个编辑区。
// 两个区块独立加载、独立保存；未保存的内容在切换标签页时保留。
import { useEffect, useState } from "react";
import {
  apiFetch,
  jsonRequest,
  checkAutoRebuild,
  deleteAutoRebuild,
  getAutoRebuild,
  setAutoRebuild,
} from "../../api";
import Modal from "../Modal";
import { Button, FormField } from "../ui";

const STATUS_LABELS = {
  ok: "连通",
  blocked: "疑似被墙",
  rebuilding: "重建换 IP 中",
  error: "检测出错",
  idle: "未检测（实例未运行）",
};

// 创建实例后由面板后台执行开机脚本，这里展示最近一次执行状态。
const SCRIPT_RUN_LABELS = {
  pending: "等待实例启动…",
  running: "正在执行…",
  success: "上次创建后执行成功",
  failed: "上次创建后执行失败",
};

function fmtTime(s) {
  if (!s) return "—";
  const d = new Date(s);
  if (isNaN(d.getTime())) return s;
  return d.toLocaleString();
}

// ---------- 开机脚本区块 ----------
function ScriptSection({ inst, onChanged }) {
  const [script, setScript] = useState("");
  const [configured, setConfigured] = useState(false);
  const [lastRun, setLastRun] = useState(null); // 创建后最近一次执行状态
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [status, setStatus] = useState(null); // { msg, cls }
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let alive = true;
    apiFetch("/api/instances/" + inst.id + "/script")
      .then((data) => {
        if (!alive) return;
        setScript((data && data.script) || "");
        setConfigured(data && data.configured === true);
        setLastRun((data && data.last_run) || null);
        setLoading(false);
      })
      .catch((err) => {
        if (!alive) return;
        setError(err && err.message ? err.message : "加载失败");
        setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [inst.id]);

  async function save() {
    if (!script.trim()) {
      setStatus({ msg: "脚本不能为空", cls: "error" });
      return;
    }
    setBusy(true);
    setStatus({ msg: "保存中…", cls: "" });
    try {
      await jsonRequest("/api/instances/" + inst.id + "/script", "PUT", { script });
      setConfigured(true);
      setStatus({ msg: "已保存", cls: "ok" });
      if (onChanged) onChanged();
    } catch (err) {
      setStatus({ msg: "保存失败：" + (err.message || "未知错误"), cls: "error" });
    } finally {
      setBusy(false);
    }
  }

  async function reset() {
    setBusy(true);
    setStatus({ msg: "", cls: "" });
    try {
      await apiFetch("/api/instances/" + inst.id + "/script", { method: "DELETE" });
      const data = await apiFetch("/api/instances/" + inst.id + "/script");
      setScript((data && data.script) || "");
      setConfigured(false);
      setStatus({ msg: "已恢复默认脚本", cls: "ok" });
      if (onChanged) onChanged();
    } catch (err) {
      setStatus({ msg: "恢复失败：" + (err.message || "未知错误"), cls: "error" });
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      {loading && <div className="boot-script-status">正在加载…</div>}
      {error && <div className="boot-script-status error">{error}</div>}
      <p className="boot-script-hint">
        {configured
          ? "当前为该实例配置了自定义脚本；创建实例时由面板在实例启动后通过 SSH 执行，重建时也会执行。"
          : "当前未配置自定义脚本，重建时使用内置默认脚本。"}
      </p>
      {lastRun && (
        <div
          className={
            "boot-script-status " +
            (lastRun.state === "failed" ? "error" : lastRun.state === "success" ? "ok" : "")
          }
        >
          {SCRIPT_RUN_LABELS[lastRun.state] || lastRun.state}
          {lastRun.host ? "（" + lastRun.host + "）" : ""}
          {lastRun.finished_at ? " · " + fmtTime(lastRun.finished_at) : ""}
          {lastRun.error ? "：" + lastRun.error : ""}
        </div>
      )}
      <textarea
        className="boot-script-textarea"
        rows={12}
        spellCheck={false}
        placeholder={"#!/usr/bin/env bash\n# 写入重建后要执行的命令…"}
        value={script}
        onChange={(e) => setScript(e.target.value)}
        disabled={loading || !!error}
      />
      <div className="boot-script-actions">
        <span className={"boot-script-status" + (status ? " " + status.cls : "")}>
          {status ? status.msg : ""}
        </span>
        <button className="btn btn-primary" type="button" onClick={save} disabled={busy || loading}>
          保存
        </button>
        {configured && (
          <button className="btn btn-ghost" type="button" onClick={reset} disabled={busy || loading}>
            恢复默认
          </button>
        )}
      </div>
    </>
  );
}

// ---------- 自动换 IP 区块 ----------
function AutoRebuildSection({ inst, onChanged }) {
  const [enabled, setEnabled] = useState(false);
  const [port, setPort] = useState("22");
  const [threshold, setThreshold] = useState("3");
  const [intervalMin, setIntervalMin] = useState("5");
  const [state, setState] = useState(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState(null); // { msg, cls }
  const [error, setError] = useState("");

  useEffect(() => {
    let alive = true;
    getAutoRebuild(inst.id)
      .then((data) => {
        if (!alive) return;
        setState(data);
        setEnabled(!!data.enabled);
        setPort(String(data.port || 22));
        setThreshold(String(data.threshold || 3));
        setIntervalMin(String(Math.round((data.interval_seconds || 300) / 60) || 5));
        setLoading(false);
      })
      .catch((err) => {
        if (!alive) return;
        setError((err && err.message) || "加载失败");
        setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [inst.id]);

  const tokenId = inst && inst.token_id != null ? inst.token_id : undefined;

  async function save() {
    const p = parseInt(port, 10);
    const t = parseInt(threshold, 10);
    const m = parseInt(intervalMin, 10);
    if (!(p >= 1 && p <= 65535)) {
      setStatus({ msg: "端口必须在 1-65535 之间", cls: "error" });
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
    setBusy(true);
    setStatus({ msg: "保存中…", cls: "" });
    try {
      const data = await setAutoRebuild(inst.id, {
        enabled,
        port: p,
        threshold: t,
        interval_seconds: m * 60,
        token_id: tokenId,
      });
      setState(data);
      setStatus({ msg: enabled ? "已开启自动换 IP" : "已保存（未开启）", cls: "ok" });
      if (onChanged) onChanged();
    } catch (err) {
      setStatus({ msg: "保存失败：" + ((err && err.message) || "未知错误"), cls: "error" });
    } finally {
      setBusy(false);
    }
  }

  async function disable() {
    setBusy(true);
    setStatus({ msg: "处理中…", cls: "" });
    try {
      await deleteAutoRebuild(inst.id);
      setEnabled(false);
      setState(null);
      setStatus({ msg: "已关闭并移除配置", cls: "ok" });
      if (onChanged) onChanged();
    } catch (err) {
      setStatus({ msg: "关闭失败：" + ((err && err.message) || "未知错误"), cls: "error" });
    } finally {
      setBusy(false);
    }
  }

  async function checkNow() {
    setBusy(true);
    setStatus({ msg: "检测中…", cls: "" });
    try {
      const data = await checkAutoRebuild(inst.id);
      setState(data);
      const label = STATUS_LABELS[data.last_status] || data.last_status || "";
      setStatus({ msg: "检测完成：" + label, cls: "ok" });
      if (onChanged) onChanged();
    } catch (err) {
      setStatus({ msg: "检测失败：" + ((err && err.message) || "未知错误"), cls: "error" });
    } finally {
      setBusy(false);
    }
  }

  const configured = state != null;

  return (
    <>
      {loading && <div className="boot-script-status">正在加载…</div>}
      {error && <div className="boot-script-status error">{error}</div>}
      {!loading && !error && (
        <>
          <p className="boot-script-hint">
            开启后，面板会定期从国内探测该实例的端口；连续失败达到阈值即自动「删机重建」，
            实例换到新的公网 IP。<strong>重建会删除旧实例及数据</strong>，请确保已备份。
          </p>
          <FormField label="开启自动换 IP">
            <label className="exec-option">
              <input
                type="checkbox"
                checked={enabled}
                onChange={(e) => setEnabled(e.target.checked)}
                disabled={busy}
              />
              <span className="exec-option-text">
                <span>被墙后自动删机重建换 IP</span>
                <span className="exec-option-sub">使用该实例配置的开机脚本；未配置则用内置默认脚本</span>
              </span>
            </label>
          </FormField>
          <FormField label="检测端口（TCP）" htmlFor="ar-port">
            <input
              id="ar-port"
              className="modal-input"
              type="number"
              min="1"
              max="65535"
              value={port}
              onChange={(e) => setPort(e.target.value)}
              disabled={busy}
            />
          </FormField>
          <FormField label="连续失败多少次判定被墙" htmlFor="ar-threshold">
            <input
              id="ar-threshold"
              className="modal-input"
              type="number"
              min="1"
              max="20"
              value={threshold}
              onChange={(e) => setThreshold(e.target.value)}
              disabled={busy}
            />
          </FormField>
          <FormField label="检测间隔（分钟）" htmlFor="ar-interval">
            <input
              id="ar-interval"
              className="modal-input"
              type="number"
              min="1"
              max="1440"
              value={intervalMin}
              onChange={(e) => setIntervalMin(e.target.value)}
              disabled={busy}
            />
          </FormField>

          {configured && (
            <div className="modal-detail auto-rebuild-state">
              <div>当前状态：{STATUS_LABELS[state.last_status] || state.last_status || "—"}</div>
              <div>连续失败：{state.fail_count || 0}</div>
              <div>最近检测：{fmtTime(state.last_check_at)}</div>
              <div>最近被墙：{fmtTime(state.last_blocked_at)}</div>
              {state.last_task_id ? <div>最近任务：{state.last_task_id}</div> : null}
              {state.last_detail ? <div>详情：{state.last_detail}</div> : null}
            </div>
          )}

          <div className="modal-actions">
            <span className={"boot-script-status" + (status ? " " + status.cls : "")}>
              {status ? status.msg : ""}
            </span>
            {configured && (
              <Button variant="ghost" onClick={checkNow} disabled={busy}>
                立即检测
              </Button>
            )}
            {configured && (
              <Button variant="danger" onClick={disable} disabled={busy}>
                关闭
              </Button>
            )}
            <Button variant="primary" onClick={save} disabled={busy}>
              保存
            </Button>
          </div>
        </>
      )}
    </>
  );
}

// ---------- root 密码区块 ----------
function RootPasswordSection({ inst, onChanged }) {
  const [configured, setConfigured] = useState(false);
  const [password, setPassword] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [status, setStatus] = useState(null); // { msg, cls }
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let alive = true;
    apiFetch("/api/instances/" + inst.id + "/root-password")
      .then((data) => {
        if (!alive) return;
        setConfigured(!!(data && data.configured));
        setLoading(false);
      })
      .catch((err) => {
        if (!alive) return;
        setError(err && err.message ? err.message : "加载失败");
        setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [inst.id]);

  async function save() {
    if (!password.trim()) {
      setStatus({ msg: "密码不能为空", cls: "error" });
      return;
    }
    setBusy(true);
    setStatus({ msg: "保存中…", cls: "" });
    try {
      await jsonRequest("/api/instances/" + inst.id + "/root-password", "PUT", { password });
      setConfigured(true);
      setPassword("");
      setStatus({ msg: "已保存", cls: "ok" });
      if (onChanged) onChanged();
    } catch (err) {
      setStatus({ msg: "保存失败：" + ((err && err.message) || "未知错误"), cls: "error" });
    } finally {
      setBusy(false);
    }
  }

  async function clear() {
    setBusy(true);
    setStatus({ msg: "", cls: "" });
    try {
      await apiFetch("/api/instances/" + inst.id + "/root-password", { method: "DELETE" });
      setConfigured(false);
      setPassword("");
      setStatus({ msg: "已清除", cls: "ok" });
      if (onChanged) onChanged();
    } catch (err) {
      setStatus({ msg: "清除失败：" + ((err && err.message) || "未知错误"), cls: "error" });
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      {loading && <div className="boot-script-status">正在加载…</div>}
      {error && <div className="boot-script-status error">{error}</div>}
      {!loading && !error && (
        <>
          <p className="boot-script-hint">
            密码保存在面板数据库中（数据库文件权限 0600），删机重建时会用同一密码创建新实例；
            面板不会回显已保存的密码。未保存密码时，重建后的新实例仅使用一次性 SSH 密钥登录。
          </p>
          <div className="boot-script-status">
            {configured ? "当前已保存 root 密码（不回显）" : "当前未保存 root 密码"}
          </div>
          <FormField label={configured ? "设置新密码" : "设置 root 密码"} htmlFor="root-pass-input">
            <input
              id="root-pass-input"
              className="modal-input"
              type="password"
              autoComplete="new-password"
              placeholder={configured ? "输入新密码以替换已保存的密码" : "输入 root 密码"}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              disabled={busy}
            />
          </FormField>
          <div className="modal-actions">
            <span className={"boot-script-status" + (status ? " " + status.cls : "")}>
              {status ? status.msg : ""}
            </span>
            {configured && (
              <button className="btn btn-ghost" type="button" onClick={clear} disabled={busy}>
                清除密码
              </button>
            )}
            <button className="btn btn-primary" type="button" onClick={save} disabled={busy}>
              保存
            </button>
          </div>
        </>
      )}
    </>
  );
}

// ---------- 合并模态框 ----------
export default function InstanceEditModal({
  inst,
  canScript,
  canAutoRebuild,
  onClose,
  onScriptChanged,
  onAutoRebuildChanged,
}) {
  const tabs = [];
  if (canScript) tabs.push(["script", "开机脚本"], ["password", "root 密码"]);
  if (canAutoRebuild) tabs.push(["auto", "自动换 IP"]);
  const showTabs = tabs.length > 1;
  const [tab, setTab] = useState(canScript ? "script" : "auto");

  return (
    <Modal title={"编辑 · " + (inst.label || "#" + inst.id)} onClose={onClose}>
      {showTabs && (
        <div className="edit-modal-tabs">
          {tabs.map(([key, label]) => (
            <button
              key={key}
              type="button"
              className={"edit-modal-tab" + (tab === key ? " active" : "")}
              onClick={() => setTab(key)}
            >
              {label}
            </button>
          ))}
        </div>
      )}
      <div style={{ display: canScript && (tab === "script" || !showTabs) ? "block" : "none" }}>
        {canScript && <ScriptSection inst={inst} onChanged={onScriptChanged} />}
      </div>
      <div style={{ display: canScript && tab === "password" ? "block" : "none" }}>
        {canScript && <RootPasswordSection inst={inst} onChanged={onScriptChanged} />}
      </div>
      <div style={{ display: canAutoRebuild && (tab === "auto" || !showTabs) ? "block" : "none" }}>
        {canAutoRebuild && <AutoRebuildSection inst={inst} onChanged={onAutoRebuildChanged} />}
      </div>
      <div className="boot-script-actions">
        <button className="btn btn-ghost" type="button" onClick={onClose}>
          关闭
        </button>
      </div>
    </Modal>
  );
}
