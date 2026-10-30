// 操作确认模态框：确认开机/关机/重启，支持面板直发或经 Agent 执行。
import { useEffect, useState } from "react";
import { useAuth } from "../../auth";
import { apiFetch } from "../../api";
import { ACTION_LABELS } from "../../constants";
import Modal from "../Modal";
import { Button, FormField } from "../ui";

export default function ActionModal({ inst, action, onClose, onConfirm }) {
  const { hasPerm } = useAuth();
  const [mode, setMode] = useState("direct");
  const [agents, setAgents] = useState([]);
  const [agentId, setAgentId] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!hasPerm("admin")) return;
    let alive = true;
    apiFetch("/api/agents")
      .then((data) => {
        if (alive) setAgents(Array.isArray(data.agents) ? data.agents : []);
      })
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, [hasPerm]);

  async function confirm() {
    setError("");
    setBusy(true);
    try {
      await onConfirm({ viaAgent: mode === "agent", agentId });
      onClose();
    } catch (err) {
      setError(err && err.message ? err.message : "操作失败");
      setBusy(false);
    }
  }

  const label = ACTION_LABELS[action] || action;

  return (
    <Modal title={"确认" + label} onClose={onClose}>
      <p className="modal-detail">
        将对实例「{inst.label || "#" + inst.id}」执行{label}操作。
      </p>
      <FormField label="执行方式">
        <div className="exec-select">
          <label className="exec-option">
            <input
              type="radio"
              name="exec-mode"
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
                  name="exec-mode"
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
        <Button variant="primary" onClick={confirm} disabled={busy}>
          确认
        </Button>
      </div>
      <div className="modal-error">{error}</div>
    </Modal>
  );
}
