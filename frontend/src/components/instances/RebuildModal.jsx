// 重建确认模态框：开机脚本（预填该实例配置，未配置用全局默认）。
// 执行方式固定跟随账号绑定：账号绑定了 Agent 就经该 Agent 执行，否则面板直发。
import { useEffect, useState } from "react";
import { apiFetch } from "../../api";
import Modal from "../Modal";
import { Button, FormField } from "../ui";
import { regionLabel, typeLabel } from "../../utils/linodeLabels";

export default function RebuildModal({ inst, boundAgentName, onClose, onConfirm }) {
  const [script, setScript] = useState("");
  const [rootPassword, setRootPassword] = useState("");
  const [passwordConfigured, setPasswordConfigured] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let alive = true;
    // 预填该实例的开机脚本；未配置时后端返回全局默认脚本。
    apiFetch("/api/instances/" + inst.id + "/script")
      .then((data) => {
        if (alive) setScript((data && data.script) || "");
      })
      .catch(() => {
        if (alive) {
          setScript("");
          setError("加载开机脚本失败，请直接填写。");
        }
      });
    apiFetch("/api/instances/" + inst.id + "/root-password")
      .then((data) => {
        if (alive) setPasswordConfigured(!!(data && data.configured));
      })
      .catch(() => {
        /* 密码状态仅用于提示，加载失败时按未保存处理 */
      });
    return () => {
      alive = false;
    };
  }, [inst.id]);

  async function confirm() {
    if (!script.trim()) {
      setError("开机脚本不能为空");
      return;
    }
    setError("");
    setBusy(true);
    try {
      // 不传 viaAgent，由后端按账号绑定决定执行通道。
      await onConfirm({ script, root_password: rootPassword.trim() || undefined });
      onClose();
    } catch (err) {
      setError(err && err.message ? err.message : "重建失败");
      setBusy(false);
    }
  }

  const channel = boundAgentName
    ? "经 Agent「" + boundAgentName + "」执行"
    : "面板直发执行";

  return (
    <Modal title="重建实例" onClose={onClose}>
      <p className="modal-warning">将删除并重建该实例，所有数据丢失！</p>
      <p className="modal-detail">
        将保留地区 / 机型 / 镜像：地区={inst.region ? regionLabel(inst.region) : "—"}
        ，机型={inst.type ? typeLabel(inst.type) : "—"}
        ，image={inst.image || "—"}。重建后新实例的 IP 不保证与原来相同。
      </p>
      <p className="modal-detail">执行方式：{channel}（跟随账号绑定）</p>
      <FormField
        label="开机脚本（重建后自动执行，并保存为该实例的配置）"
        htmlFor="rebuild-script-input"
      >
        <textarea
          id="rebuild-script-input"
          className="modal-textarea"
          rows={12}
          spellCheck={false}
          placeholder={"#!/usr/bin/env bash\n# 写入重建后要执行的命令…"}
          value={script}
          onChange={(e) => setScript(e.target.value)}
        />
      </FormField>
      <FormField label="root 密码（可选）" htmlFor="rebuild-rootpass-input">
        <input
          id="rebuild-rootpass-input"
          className="modal-input"
          type="password"
          autoComplete="new-password"
          placeholder={passwordConfigured ? "留空则使用已保存的密码" : "留空则仅使用 SSH 密钥登录"}
          value={rootPassword}
          onChange={(e) => setRootPassword(e.target.value)}
          disabled={busy}
        />
        <p className="boot-script-hint">
          填写后会保存为该实例的 root 密码，重建时用于创建新实例；
          {passwordConfigured
            ? "该实例已保存密码，留空即使用已保存的密码。"
            : "该实例尚未保存密码，留空则新实例仅使用一次性 SSH 密钥登录。"}
        </p>
      </FormField>
      <div className="modal-actions">
        <Button variant="ghost" onClick={onClose} disabled={busy}>
          取消
        </Button>
        <Button variant="danger" onClick={confirm} disabled={busy}>
          确认重建
        </Button>
      </div>
      <div className="modal-error">{error}</div>
    </Modal>
  );
}
