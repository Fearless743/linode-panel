import { useEffect, useState } from "react";
import { apiFetch, jsonRequest } from "../api";
import Modal from "./Modal";

// 为单个实例配置开机脚本；未配置时使用内置默认脚本。
export default function InstanceScriptModal({ inst, onClose, onChanged }) {
  const [script, setScript] = useState("");
  const [configured, setConfigured] = useState(false);
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
    <Modal title={"开机脚本 · " + (inst.label || "#" + inst.id)} onClose={onClose}>
      {loading && <div className="boot-script-status">正在加载…</div>}
      {error && <div className="boot-script-status error">{error}</div>}
      <p className="boot-script-hint">
        {configured
          ? "当前为该实例配置了自定义脚本，重建时使用此脚本。"
          : "当前未配置自定义脚本，重建时使用内置默认脚本。"}
      </p>
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
        <button className="btn btn-ghost" type="button" onClick={onClose} disabled={busy}>
          关闭
        </button>
      </div>
    </Modal>
  );
}
