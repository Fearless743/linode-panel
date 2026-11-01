import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import Modal from "../Modal";
import { apiFetch, getInstanceConfig, setInstanceConfig } from "../../api";

// ---------- 账号组目标开机配置模态框 ----------
// 加载时调用 getInstanceConfig 回填表单；保存时调用 setInstanceConfig。
// 清除按钮调用 setInstanceConfig(groupId, null)。
// 支持从实例缓存（页面上已加载的账号实例）中选择一个来预填配置。
export default function InstanceConfigModal({ group, instances, onClose, onChanged }) {
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

  // 从已有缓存实例填充：选中项 + 填充结果提示。
  const [sourceId, setSourceId] = useState("");
  const [fillNotice, setFillNotice] = useState("");
  // 防止快速切换来源时，较早的脚本请求覆盖较新的选择。
  const sourceReq = useRef(0);

  // 可选来源：页面上已加载的实例缓存（后端数据库快照）。
  const sources = useMemo(() => {
    const list = Array.isArray(instances) ? instances : [];
    return [...list].sort((a, b) => {
      const labelCmp = String(a.label || "").localeCompare(String(b.label || ""));
      return labelCmp !== 0 ? labelCmp : Number(a.id) - Number(b.id);
    });
  }, [instances]);

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

  // 选中缓存实例后，用其 region/type/image/label 预填表单，并尝试读取其开机脚本。
  async function applySource(value) {
    const req = ++sourceReq.current;
    setSourceId(value);
    setFillNotice("");
    setError("");
    if (!value) return;
    const inst = sources.find((i) => String(i.id) === String(value));
    if (!inst) return;

    setRegion(inst.region || "");
    setType(inst.type || "");
    setImage(inst.image || "");
    setLabel(inst.label || "");

    const name = inst.label || "#" + inst.id;
    try {
      const data = await apiFetch(
        "/api/instances/" + encodeURIComponent(inst.id) + "/script"
      );
      if (req !== sourceReq.current) return;
      if (data && data.configured) {
        setScript(data.script || "");
        setFillNotice("已从实例「" + name + "」回填配置和开机脚本");
      } else {
        setFillNotice(
          "已从实例「" + name + "」回填地区/机型/镜像/名称；该实例未配置开机脚本，脚本保持为空"
        );
      }
    } catch {
      if (req !== sourceReq.current) return;
      setFillNotice(
        "已从实例「" +
          name +
          "」回填地区/机型/镜像/名称；读取开机脚本失败（可能需要 script 权限），请手动填写脚本"
      );
    }
  }

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
            <label htmlFor="ag-ic-source">从已有缓存实例填充</label>
            <select
              id="ag-ic-source"
              className="modal-input"
              value={sourceId}
              onChange={(e) => applySource(e.target.value)}
              disabled={busy}
            >
              <option value="">
                {sources.length === 0
                  ? "（暂无缓存实例，可手动填写）"
                  : "选择实例，自动回填配置…"}
              </option>
              {sources.map((inst) => (
                <option key={inst.id} value={String(inst.id)}>
                  {(inst.label || "实例 #" + inst.id) +
                    " · " +
                    (inst.region || "?") +
                    " / " +
                    (inst.type || "?") +
                    " #" +
                    inst.id +
                    (inst.token_label ? "（" + inst.token_label + "）" : "")}
                </option>
              ))}
            </select>
            <p className="boot-script-hint">
              选项来自实例列表缓存；选择后回填地区/机型/镜像/名称，若该实例已配置开机脚本则一并回填。
            </p>
          </div>
          {fillNotice && <div className="boot-script-status ok">{fillNotice}</div>}
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
