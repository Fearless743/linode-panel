// 实例「编辑」模态框：合并开机脚本、root 密码与自动换 IP 三个编辑区。
// 三个区块独立加载，但共用底部的「保存」按钮一次性提交全部改动；
// 未修改的区块不会写入（避免把默认脚本固化成自定义脚本等副作用）。
import {
  forwardRef,
  useCallback,
  useEffect,
  useImperativeHandle,
  useRef,
  useState,
} from "react";
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
const ScriptSection = forwardRef(function ScriptSection(
  { inst, disabled, onChanged, onReady },
  ref
) {
  const [script, setScript] = useState("");
  const [configured, setConfigured] = useState(false);
  const [lastRun, setLastRun] = useState(null); // 创建后最近一次执行状态
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [status, setStatus] = useState(null); // { msg, cls }
  const [busy, setBusy] = useState(false);
  // 加载时的脚本内容，用于判断用户是否真的改过（未改则保存时跳过）。
  const initialRef = useRef("");

  useEffect(() => {
    let alive = true;
    apiFetch("/api/instances/" + inst.id + "/script")
      .then((data) => {
        if (!alive) return;
        const value = (data && data.script) || "";
        initialRef.current = value;
        setScript(value);
        setConfigured(data && data.configured === true);
        setLastRun((data && data.last_run) || null);
        setLoading(false);
      })
      .catch((err) => {
        if (!alive) return;
        setError(err && err.message ? err.message : "加载失败");
        setLoading(false);
      })
      .finally(() => {
        if (alive && onReady) onReady();
      });
    return () => {
      alive = false;
    };
  }, [inst.id, onReady]);

  // 统一保存入口：有改动才提交。
  async function save() {
    setStatus(null);
    if (loading) return { ok: false, msg: "开机脚本尚未加载完成" };
    if (error) return { ok: false, msg: "开机脚本加载失败，无法保存" };
    if (script === initialRef.current) return { ok: true, changed: false };
    if (!script.trim()) return { ok: false, msg: "开机脚本不能为空" };
    try {
      await jsonRequest("/api/instances/" + inst.id + "/script", "PUT", { script });
      initialRef.current = script;
      setConfigured(true);
      if (onChanged) onChanged();
      return { ok: true, changed: true, label: "开机脚本" };
    } catch (err) {
      return {
        ok: false,
        msg: "开机脚本保存失败：" + (err.message || "未知错误"),
      };
    }
  }

  useImperativeHandle(ref, () => ({ save }));

  async function reset() {
    setBusy(true);
    setStatus({ msg: "", cls: "" });
    try {
      await apiFetch("/api/instances/" + inst.id + "/script", { method: "DELETE" });
      const data = await apiFetch("/api/instances/" + inst.id + "/script");
      const value = (data && data.script) || "";
      initialRef.current = value;
      setScript(value);
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
        disabled={loading || !!error || disabled}
      />
      <div className="boot-script-actions">
        <span className={"boot-script-status" + (status ? " " + status.cls : "")}>
          {status ? status.msg : ""}
        </span>
        {configured && (
          <button
            className="btn btn-ghost"
            type="button"
            onClick={reset}
            disabled={busy || disabled || loading}
          >
            恢复默认
          </button>
        )}
      </div>
    </>
  );
});

// ---------- 自动换 IP 区块 ----------
const AutoRebuildSection = forwardRef(function AutoRebuildSection(
  { inst, disabled, onChanged, onReady },
  ref
) {
  const [enabled, setEnabled] = useState(false);
  const [port, setPort] = useState("22");
  const [threshold, setThreshold] = useState("3");
  const [intervalMin, setIntervalMin] = useState("5");
  const [state, setState] = useState(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState(null); // { msg, cls }
  const [error, setError] = useState("");
  // 加载时的表单值，用于判断是否真的有改动。
  const initialRef = useRef(null);

  useEffect(() => {
    let alive = true;
    getAutoRebuild(inst.id)
      .then((data) => {
        if (!alive) return;
        const portValue = data.port || 22;
        const thresholdValue = data.threshold || 3;
        const intervalValue = Math.round((data.interval_seconds || 300) / 60) || 5;
        setState(data);
        setEnabled(!!data.enabled);
        setPort(String(portValue));
        setThreshold(String(thresholdValue));
        setIntervalMin(String(intervalValue));
        initialRef.current = {
          enabled: !!data.enabled,
          port: portValue,
          threshold: thresholdValue,
          intervalMin: intervalValue,
        };
        setLoading(false);
      })
      .catch((err) => {
        if (!alive) return;
        setError((err && err.message) || "加载失败");
        setLoading(false);
      })
      .finally(() => {
        if (alive && onReady) onReady();
      });
    return () => {
      alive = false;
    };
  }, [inst.id, onReady]);

  const tokenId = inst && inst.token_id != null ? inst.token_id : undefined;

  // 统一保存入口：有改动才提交。
  async function save() {
    setStatus(null);
    if (loading) return { ok: false, msg: "自动换 IP 配置尚未加载完成" };
    if (error) return { ok: false, msg: "自动换 IP 配置加载失败，无法保存" };
    const p = parseInt(port, 10);
    const t = parseInt(threshold, 10);
    const m = parseInt(intervalMin, 10);
    if (!(p >= 1 && p <= 65535)) {
      return { ok: false, msg: "检测端口必须在 1-65535 之间" };
    }
    if (!(t >= 1 && t <= 20)) {
      return { ok: false, msg: "连续失败次数必须在 1-20 之间" };
    }
    if (!(m >= 1 && m <= 1440)) {
      return { ok: false, msg: "检测间隔必须在 1-1440 分钟之间" };
    }
    const init = initialRef.current;
    if (
      init &&
      init.enabled === enabled &&
      init.port === p &&
      init.threshold === t &&
      init.intervalMin === m
    ) {
      return { ok: true, changed: false };
    }
    try {
      const data = await setAutoRebuild(inst.id, {
        enabled,
        port: p,
        threshold: t,
        interval_seconds: m * 60,
        token_id: tokenId,
      });
      setState(data);
      initialRef.current = { enabled: !!data.enabled, port: p, threshold: t, intervalMin: m };
      if (onChanged) onChanged();
      return { ok: true, changed: true, label: "自动换 IP" };
    } catch (err) {
      return {
        ok: false,
        msg: "自动换 IP 保存失败：" + ((err && err.message) || "未知错误"),
      };
    }
  }

  useImperativeHandle(ref, () => ({ save }));

  async function disable() {
    setBusy(true);
    setStatus({ msg: "处理中…", cls: "" });
    try {
      await deleteAutoRebuild(inst.id);
      setEnabled(false);
      setState(null);
      // 配置已删除，同步基线，避免随后「保存全部」又把配置写回。
      initialRef.current = {
        enabled: false,
        port: parseInt(port, 10),
        threshold: parseInt(threshold, 10),
        intervalMin: parseInt(intervalMin, 10),
      };
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
  const actionDisabled = busy || disabled;

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
                disabled={actionDisabled}
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
              disabled={actionDisabled}
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
              disabled={actionDisabled}
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
              disabled={actionDisabled}
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
              <Button variant="ghost" onClick={checkNow} disabled={actionDisabled}>
                立即检测
              </Button>
            )}
            {configured && (
              <Button variant="danger" onClick={disable} disabled={actionDisabled}>
                关闭
              </Button>
            )}
          </div>
        </>
      )}
    </>
  );
});

// ---------- root 密码区块 ----------
const RootPasswordSection = forwardRef(function RootPasswordSection(
  { inst, disabled, onChanged, onReady },
  ref
) {
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
      })
      .finally(() => {
        if (alive && onReady) onReady();
      });
    return () => {
      alive = false;
    };
  }, [inst.id, onReady]);

  // 统一保存入口：密码是只写字段，只有填了新密码才提交。
  async function save() {
    setStatus(null);
    if (loading) return { ok: false, msg: "root 密码状态尚未加载完成" };
    if (error) return { ok: false, msg: "root 密码状态加载失败，无法保存" };
    if (!password.trim()) return { ok: true, changed: false };
    try {
      await jsonRequest("/api/instances/" + inst.id + "/root-password", "PUT", { password });
      setConfigured(true);
      setPassword("");
      if (onChanged) onChanged();
      return { ok: true, changed: true, label: "root 密码" };
    } catch (err) {
      return {
        ok: false,
        msg: "root 密码保存失败：" + ((err && err.message) || "未知错误"),
      };
    }
  }

  useImperativeHandle(ref, () => ({ save }));

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
              disabled={busy || disabled}
            />
          </FormField>
          <div className="modal-actions">
            <span className={"boot-script-status" + (status ? " " + status.cls : "")}>
              {status ? status.msg : ""}
            </span>
            {configured && (
              <button
                className="btn btn-ghost"
                type="button"
                onClick={clear}
                disabled={busy || disabled}
              >
                清除密码
              </button>
            )}
          </div>
        </>
      )}
    </>
  );
});

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

  const scriptRef = useRef(null);
  const passwordRef = useRef(null);
  const autoRef = useRef(null);
  const savingRef = useRef(false);

  // 各区块是否已完成加载（加载完成才允许保存，避免提交占位值）。
  const [ready, setReady] = useState({ script: false, password: false, auto: false });
  const [busy, setBusy] = useState(false);
  const [saveStatus, setSaveStatus] = useState(null); // { msg, cls }

  const markScriptReady = useCallback(
    () => setReady((prev) => (prev.script ? prev : { ...prev, script: true })),
    []
  );
  const markPasswordReady = useCallback(
    () => setReady((prev) => (prev.password ? prev : { ...prev, password: true })),
    []
  );
  const markAutoReady = useCallback(
    () => setReady((prev) => (prev.auto ? prev : { ...prev, auto: true })),
    []
  );

  const allReady =
    (!canScript || (ready.script && ready.password)) && (!canAutoRebuild || ready.auto);

  // 一个保存按钮提交全部改动：逐个调用区块的 save()，汇总结果。
  const saveAll = async () => {
    if (savingRef.current) return;
    savingRef.current = true;
    setBusy(true);
    setSaveStatus({ msg: "保存中…", cls: "" });
    const results = [];
    for (const sectionRef of [scriptRef, passwordRef, autoRef]) {
      if (!sectionRef.current) continue;
      let res;
      try {
        res = await sectionRef.current.save();
      } catch (err) {
        res = { ok: false, msg: (err && err.message) || "保存失败" };
      }
      if (res) results.push(res);
    }
    const errors = results.filter((r) => !r.ok);
    const saved = results.filter((r) => r.ok && r.changed);
    if (errors.length > 0) {
      setSaveStatus({ msg: errors.map((e) => e.msg).join("；"), cls: "error" });
    } else if (saved.length > 0) {
      setSaveStatus({
        msg: "已保存：" + saved.map((s) => s.label).join("、"),
        cls: "ok",
      });
    } else {
      setSaveStatus({ msg: "没有需要保存的修改", cls: "" });
    }
    savingRef.current = false;
    setBusy(false);
  };

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
        {canScript && (
          <ScriptSection
            ref={scriptRef}
            inst={inst}
            disabled={busy}
            onChanged={onScriptChanged}
            onReady={markScriptReady}
          />
        )}
      </div>
      <div style={{ display: canScript && tab === "password" ? "block" : "none" }}>
        {canScript && (
          <RootPasswordSection
            ref={passwordRef}
            inst={inst}
            disabled={busy}
            onChanged={onScriptChanged}
            onReady={markPasswordReady}
          />
        )}
      </div>
      <div style={{ display: canAutoRebuild && (tab === "auto" || !showTabs) ? "block" : "none" }}>
        {canAutoRebuild && (
          <AutoRebuildSection
            ref={autoRef}
            inst={inst}
            disabled={busy}
            onChanged={onAutoRebuildChanged}
            onReady={markAutoReady}
          />
        )}
      </div>
      <div className="modal-actions">
        <span className={"boot-script-status" + (saveStatus ? " " + saveStatus.cls : "")}>
          {saveStatus ? saveStatus.msg : ""}
        </span>
        <button className="btn btn-ghost" type="button" onClick={onClose} disabled={busy}>
          关闭
        </button>
        <button
          className="btn btn-primary"
          type="button"
          onClick={saveAll}
          disabled={busy || !allReady}
        >
          保存
        </button>
      </div>
    </Modal>
  );
}
