// 重建确认模态框：保留 IP + 开机脚本（预填该实例配置，未配置用全局默认），
// 支持面板直发或经 Agent 执行。
import { useEffect, useState } from "react";
import { useAuth } from "../../auth";
import { apiFetch } from "../../api";
import Modal from "../Modal";
import { Button, FormField } from "../ui";

export default function RebuildModal({ inst, onClose, onConfirm }) {
  const { hasPerm } = useAuth();
  const [ip, setIp] = useState("");
  const [script, setScript] = useState("");
  const [mode, setMode] = useState("direct");
  const [agents, setAgents] = useState([]);
  const [agentId, setAgentId] = useState("");
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
    if (hasPerm("admin")) {
      apiFetch("/api/agents")
        .then((data) => {
          if (alive) setAgents(Array.isArray(data.agents) ? data.agents : []);
        })
        .catch(() => {});
    }
    return () => {
      alive = false;
    };
  }, [hasPerm, inst.id]);

  async function confirm() {
    if (!script.trim()) {
      setError("开机脚本不能为空");
      return;
    }
    setError("");
    setBusy(true);
    try {
      await onConfirm({ script, ip: ip.trim(), viaAgent: mode === "agent", agentId });
      onClose();
    } catch (err) {
      setError(err && err.message ? err.message : "重建失败");
      setBusy(false);
    }
  }

  return (
    <Modal title="重建实例" onClose={onClose}>
      <p className="modal-warning">将删除并重建该实例，所有数据丢失！</p>
      <p className="modal-detail">
        将保留 Region / 型号 / 镜像：region={inst.region || "—"}，type={inst.type || "—"}
        ，image={inst.image || "—"}。重建后新实例的 IP 不保证与原来相同。
      </p>
      <FormField label="保留 IP（选填，预留的 reserved IP 地址）" htmlFor="rebuild-ip-input">
        <input
          id="rebuild-ip-input"
          className="modal-input"
          type="text"
          placeholder="如 192.0.2.10，留空则不指定"
          value={ip}
          onChange={(e) => setIp(e.target.value)}
        />
      </FormField>
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
      <FormField label="执行方式">
        <div className="exec-select">
          <label className="exec-option">
            <input
              type="radio"
              name="rebuild-exec-mode"
              value="direct"
              checked={mode === "direct"}
              onChange={() => setMode("direct")}
            />
            <span className="exec-option-text">
              <span>面板直发</span>
              <span className="exec-option-sub">
                使用当前登录用户的 PAT 直接调用 Linode API
              </span>
            </span>
          </label>
          {hasPerm("admin") && (
            <>
              <label className="exec-option">
                <input
                  type="radio"
                  name="rebuild-exec-mode"
                  value="agent"
                  checked={mode === "agent"}
                  onChange={() => setMode("agent")}
                />
                <span className="exec-option-text">
                  <span>经 Agent 执行</span>
                  <span className="exec-option-sub">
                    面板加密下发 PAT，由本机 Agent 完成操作
                  </span>
                </span>
              </label>
              {mode === "agent" && (
                <select
                  className="modal-input exec-agent-select"
                  value={agentId}
                  onChange={(e) => setAgentId(e.target.value)}
                >
                  <option value="">
                    {agents.length === 0
                      ? "没有已注册的 Agent"
                      : agents.some((a) => a.local)
                        ? "本地面板（未绑定）"
                        : "未绑定（默认 Agent）"}
                  </option>
                  {agents
                    .filter((a) => !a.local)
                    .map((a) => (
                      <option key={a.id} value={String(a.id)}>
                        {a.name}
                        {a.hostname ? "（" + a.hostname + "）" : ""}
                      </option>
                    ))}
                </select>
              )}
            </>
          )}
        </div>
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
