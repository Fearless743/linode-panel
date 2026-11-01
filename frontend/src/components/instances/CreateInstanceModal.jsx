import { useEffect, useState } from "react";
import Modal from "../Modal";
import { apiFetch, jsonRequest } from "../../api";
import { regionLabel, typeLabel } from "../../utils/linodeLabels";

// ---------- 创建实例模态框 ----------
// 账号切换时加载可用的 region / type / image 选项。
export default function CreateInstanceModal({ tokens, onClose, onCreated }) {
  const [selToken, setSelToken] = useState(
    tokens && tokens.length > 0 ? String(tokens[0].id) : ""
  );
  const [options, setOptions] = useState(null); // { regions, types, images }
  const [optError, setOptError] = useState("");
  const [label, setLabel] = useState("");
  const [region, setRegion] = useState("");
  const [type, setType] = useState("");
  const [image, setImage] = useState("");
  const [rootPass, setRootPass] = useState("");
  const [script, setScript] = useState("");
  const [status, setStatus] = useState(null); // { msg, cls }
  const [creating, setCreating] = useState(false);

  // 弹窗打开时若账号尚未加载完成，账号就绪后默认选中第一个。
  useEffect(() => {
    if (!selToken && tokens && tokens.length > 0) {
      setSelToken(String(tokens[0].id));
    }
  }, [tokens, selToken]);

  // 选中账号变化 → 加载可用的 region/type/image。
  useEffect(() => {
    if (!selToken) return;
    let alive = true;
    setOptions(null);
    setOptError("");
    apiFetch("/api/account/options?token_id=" + encodeURIComponent(selToken))
      .then((data) => {
        if (!alive) return;
        setOptions({
          regions: Array.isArray(data.regions) ? data.regions : [],
          types: Array.isArray(data.types) ? data.types : [],
          images: Array.isArray(data.images) ? data.images : [],
        });
      })
      .catch((err) => {
        if (!alive) setOptError(err && err.message ? err.message : "加载选项失败");
      });
    return () => {
      alive = false;
    };
  }, [selToken]);

  async function create() {
    if (!selToken) {
      setStatus({ msg: "请先选择账号", cls: "error" });
      return;
    }
    if (!label.trim() || !region || !type || !image || !rootPass) {
      setStatus({ msg: "名称、地区、机型、镜像和 root 密码均不能为空", cls: "error" });
      return;
    }
    setCreating(true);
    setStatus(null);
    try {
      await jsonRequest("/api/account/instances", "POST", {
        token_id: parseInt(selToken, 10),
        label: label.trim(),
        region,
        type,
        image,
        root_pass: rootPass,
        script: script.trim(),
      });
      onCreated();
      onClose();
    } catch (err) {
      setStatus({ msg: "创建失败：" + (err.message || "未知错误"), cls: "error" });
      setCreating(false);
    }
  }

  return (
    <Modal title="创建实例" onClose={onClose}>
      <div className="modal-field">
        <label htmlFor="create-token">账号</label>
        <select
          id="create-token"
          className="modal-input"
          value={selToken}
          onChange={(e) => setSelToken(e.target.value)}
        >
          {(tokens || []).length === 0 && <option value="">（无账号，请先绑定）</option>}
          {(tokens || []).map((t) => (
            <option key={t.id} value={String(t.id)}>
              {t.label || "账号 #" + t.id}
            </option>
          ))}
        </select>
      </div>
      <div className="modal-field">
        <label htmlFor="create-label">名称</label>
        <input
          id="create-label"
          className="modal-input"
          type="text"
          placeholder="实例名称"
          value={label}
          onChange={(e) => setLabel(e.target.value)}
        />
      </div>
      {optError && <div className="modal-error">{optError}</div>}
      {options && (
        <>
          <div className="modal-field">
            <label htmlFor="create-region">地区</label>
            <select
              id="create-region"
              className="modal-input"
              value={region}
              onChange={(e) => setRegion(e.target.value)}
            >
              <option value="">选择地区…</option>
              {options.regions.map((r) => (
                <option key={r.id} value={r.id}>
                  {regionLabel(r.id, r.label)}
                </option>
              ))}
            </select>
          </div>
          <div className="modal-field">
            <label htmlFor="create-type">机型</label>
            <select
              id="create-type"
              className="modal-input"
              value={type}
              onChange={(e) => setType(e.target.value)}
            >
              <option value="">选择机型…</option>
              {options.types.map((t) => (
                <option key={t.id} value={t.id}>
                  {typeLabel(t.id, t.label)}
                </option>
              ))}
            </select>
          </div>
          <div className="modal-field">
            <label htmlFor="create-image">镜像</label>
            <select
              id="create-image"
              className="modal-input"
              value={image}
              onChange={(e) => setImage(e.target.value)}
            >
              <option value="">选择镜像…</option>
              {options.images.map((img) => (
                <option key={img.id} value={img.id}>
                  {img.label || img.id}
                </option>
              ))}
            </select>
          </div>
        </>
      )}
      <div className="modal-field">
        <label htmlFor="create-rootpass">root 密码</label>
        <input
          id="create-rootpass"
          className="modal-input"
          type="password"
          placeholder="root 密码（用于首次登录）"
          value={rootPass}
          onChange={(e) => setRootPass(e.target.value)}
        />
      </div>
      <div className="modal-field">
        <label htmlFor="create-script">开机脚本（可选）</label>
        <p className="boot-script-hint">
          创建后由面板在实例启动后通过 SSH 以 root 身份执行，并保存为该实例配置（重建时也会执行）；
          留空则不执行。
        </p>
        <textarea
          id="create-script"
          className="boot-script-textarea"
          rows={6}
          spellCheck={false}
          placeholder={"#!/usr/bin/env bash\n# 实例重建后要执行的命令…"}
          value={script}
          onChange={(e) => setScript(e.target.value)}
          disabled={creating}
        />
      </div>
      {status && (
        <div className={"boot-script-status " + status.cls} style={{ marginTop: 8 }}>
          {status.msg}
        </div>
      )}
      <div className="modal-actions">
        <button className="btn btn-ghost" type="button" onClick={onClose} disabled={creating}>
          取消
        </button>
        <button
          className="btn btn-primary"
          type="button"
          onClick={create}
          disabled={creating}
        >
          {creating ? "创建中…" : "创建实例"}
        </button>
      </div>
    </Modal>
  );
}
