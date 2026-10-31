import { useCallback, useEffect, useState } from "react";
import Modal from "../Modal";
import { getInstanceConfig, setInstanceConfig } from "../../api";

// ---------- 账号组目标开机配置模态框 ----------
// 加载时调用 getInstanceConfig 回填表单；保存时调用 setInstanceConfig。
// 清除按钮调用 setInstanceConfig(groupId, null)。
export default function InstanceConfigModal({ group, onClose, onChanged }) {
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");

  // 表单字段
  const [region, setRegion] = useState("");
  const [type, setType] = useState("");
  const [image, setImage] = useState("");
  const [label, setLabel] = useState("");
  const [script, setScript] = useState("");
  const [booted, setBooted] = useState(true);

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const res = await getInstanceConfig(group.id);
      const cfg = res && res.config;
      if (cfg) {
        setRegion(cfg.region || "");
        setType(cfg.type || "");
        setImage(cfg.image || "");
        setLabel(cfg.label || "");
        setScript(cfg.script || "");
        setBooted(cfg.booted === undefined ? true : !!cfg.booted);
      }
    } catch (err) {
      setError((err && err.message) || "加载失败");
    } finally {
      setLoading(false);
    }
  }, [group.id]);

  useEffect(() => {
    load();
  }, [load]);

  function validate() {
    if (!region.trim()) return "region 不能为空";
    if (!type.trim()) return "type 不能为空";
    if (!image.trim()) return "image 不能为空";
    if (!label.trim()) return "label 不能为空";
    return null;
  }

  async function save() {
    const validationError = validate();
    if (validationError) {
      setError(validationError);
      return;
    }
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await setInstanceConfig(group.id, {
        region: region.trim(),
        type: type.trim(),
        image: image.trim(),
        label: label.trim(),
        script: script,
        booted: booted,
      });
      setNotice("已保存目标开机配置");
      if (onChanged) onChanged();
    } catch (err) {
      setError((err && err.message) || "保存失败");
    } finally {
      setBusy(false);
    }
  }

  async function clear() {
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await setInstanceConfig(group.id, null);
      setNotice("已清除目标开机配置");
      if (onChanged) onChanged();
    } catch (err) {
      setError((err && err.message) || "清除失败");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      title={"目标开机配置 · " + (group.name || "#" + group.id)}
      onClose={onClose}
    >
      <p className="modal-detail">
        预填替换时使用的目标开机配置。组未配置时，触发替换会报 error
        「未填写账号组目标开机配置」，不会建机。
      </p>

      {loading ? (
        <div className="page-status">正在加载配置…</div>
      ) : (
        <>
          <div className="modal-field">
            <label htmlFor="ag-ic-region">Region</label>
            <input
              id="ag-ic-region"
              className="modal-input"
              type="text"
              autoComplete="off"
              placeholder="如 us-east"
              value={region}
              onChange={(e) => setRegion(e.target.value)}
              disabled={busy}
            />
          </div>
          <div className="modal-field">
            <label htmlFor="ag-ic-type">Type</label>
            <input
              id="ag-ic-type"
              className="modal-input"
              type="text"
              autoComplete="off"
              placeholder="如 g6-standard-1"
              value={type}
              onChange={(e) => setType(e.target.value)}
              disabled={busy}
            />
          </div>
          <div className="modal-field">
            <label htmlFor="ag-ic-image">Image</label>
            <input
              id="ag-ic-image"
              className="modal-input"
              type="text"
              autoComplete="off"
              placeholder="如 linode/debian12"
              value={image}
              onChange={(e) => setImage(e.target.value)}
              disabled={busy}
            />
          </div>
          <div className="modal-field">
            <label htmlFor="ag-ic-label">Label</label>
            <input
              id="ag-ic-label"
              className="modal-input"
              type="text"
              autoComplete="off"
              placeholder="实例名称"
              value={label}
              onChange={(e) => setLabel(e.target.value)}
              disabled={busy}
            />
          </div>
          <div className="modal-field">
            <label htmlFor="ag-ic-script">开机脚本</label>
            <textarea
              id="ag-ic-script"
              className="modal-input"
              rows={4}
              placeholder="#!/bin/bash&#10;echo hello"
              value={script}
              onChange={(e) => setScript(e.target.value)}
              disabled={busy}
              style={{ fontFamily: "monospace", resize: "vertical" }}
            />
          </div>
          <label className="ag-failover-toggle">
            <input
              type="checkbox"
              checked={booted}
              disabled={busy}
              onChange={(e) => setBooted(e.target.checked)}
            />
            创建后自动开机（booted）
          </label>

          <div className="modal-error">{error}</div>
          {notice && <div className="boot-script-status ok">{notice}</div>}

          <div className="modal-actions">
            <button
              className="btn btn-ghost"
              type="button"
              onClick={onClose}
              disabled={busy}
            >
              关闭
            </button>
            <button
              className="btn btn-danger btn-sm"
              type="button"
              onClick={clear}
              disabled={busy}
            >
              清除配置
            </button>
            <button
              className="btn btn-primary"
              type="button"
              onClick={save}
              disabled={busy}
            >
              {busy ? "处理中…" : "保存"}
            </button>
          </div>
        </>
      )}
    </Modal>
  );
}
