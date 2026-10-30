import { useCallback, useEffect, useState } from "react";
import { apiFetch, listAccountAgents, setAccountAgent } from "../api";
import AddTokenModal from "../components/accounts/AddTokenModal";

// 账号管理：绑定/管理自己的 Linode PAT（账号），并可为每个账号绑定
// 执行 Agent（经 Agent 执行的操作将路由到该 Agent）。
// 添加账号通过弹窗完成；创建实例入口已移至「实例」页面。
export default function Accounts() {
  const [tokens, setTokens] = useState(null); // null = 加载中
  const [agents, setAgents] = useState([]); // 可用 Agent（id + name）
  const [error, setError] = useState("");
  const [status, setStatus] = useState(null); // { msg, cls }

  // 弹窗状态。
  const [addTokenOpen, setAddTokenOpen] = useState(false);

  const loadTokens = useCallback(async () => {
    try {
      const data = await apiFetch("/api/account/tokens");
      const list = Array.isArray(data.tokens) ? data.tokens : [];
      setTokens(list);
    } catch (err) {
      setTokens([]);
      setError(err && err.message ? err.message : "加载失败");
    }
  }, []);

  useEffect(() => {
    loadTokens();
  }, [loadTokens]);

  // 加载可用 Agent 列表，用于账号绑定下拉框。
  useEffect(() => {
    let alive = true;
    listAccountAgents()
      .then((data) => {
        if (alive) setAgents(Array.isArray(data.agents) ? data.agents : []);
      })
      .catch(() => {
        // 无 Agent 时下拉框仅显示「未绑定」，忽略加载失败。
      });
    return () => {
      alive = false;
    };
  }, []);

  async function removeToken(t) {
    if (!window.confirm("确定删除账号「" + (t.label || "#" + t.id) + "」？")) return;
    setError("");
    try {
      await apiFetch("/api/account/tokens/" + t.id, { method: "DELETE" });
      await loadTokens();
    } catch (err) {
      setError(err && err.message ? err.message : "删除失败");
    }
  }

  // 绑定/解绑执行 Agent（agentId=0 表示解绑）。
  async function bindAgent(t, value) {
    const agentId = value === "" || value == null ? 0 : Number(value);
    setError("");
    setStatus(null);
    try {
      await setAccountAgent(t.id, agentId);
      const agent = agents.find((a) => a.id === agentId);
      setStatus({
        msg: agentId === 0 ? "已解绑 Agent" : "已绑定 Agent：" + (agent ? agent.name : "#" + agentId),
        cls: "ok",
      });
      // 本地更新该账号的绑定状态，避免整表刷新闪烁。
      setTokens((prev) =>
        (prev || []).map((x) =>
          x.id === t.id ? { ...x, agent_id: agentId, agent_name: agent ? agent.name : "" } : x
        )
      );
    } catch (err) {
      setError(err && err.message ? err.message : "绑定失败");
    }
  }

  // 下拉框当前值：绑定到本地面板或未绑定时统一显示为「未绑定（默认面板）」。
  function boundAgentValue(t) {
    if (!t.agent_id) return "";
    const a = agents.find((x) => x.id === t.agent_id);
    return a && !a.local ? String(a.id) : "";
  }

  return (
    <section className="panel">
      <h2 className="panel-title">账号管理</h2>
      <p className="boot-script-hint">
        每个账号对应一个 Linode PAT；绑定后可用于聚合实例、开关机与创建实例。可将账号绑定到
        某个 Agent，使「经 Agent 执行」的操作固定由该 Agent 完成。
      </p>

      {/* 账号列表 */}
      <div className="agents-section">
        <div className="agents-section-head">
          <h3 className="agents-section-title">已绑定账号</h3>
          <button
            className="btn btn-primary"
            type="button"
            onClick={() => setAddTokenOpen(true)}
          >
            绑定新账号
          </button>
        </div>
        {tokens === null && <div className="users-empty">正在加载…</div>}
        {tokens !== null && tokens.length === 0 && <div className="users-empty">尚未绑定账号</div>}
        {tokens !== null && tokens.length > 0 && (
          <table className="users-table">
            <thead>
              <tr>
                <th>名称</th>
                <th>Token</th>
                <th>执行 Agent</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {tokens.map((t) => (
                <tr key={t.id}>
                  <td>{t.label || "（未命名）"}</td>
                  <td>{t.masked || ""}</td>
                  <td>
                    <select
                      className="modal-input token-agent-select"
                      value={boundAgentValue(t)}
                      onChange={(e) => bindAgent(t, e.target.value)}
                    >
                      <option value="">
                        {agents.length === 0
                          ? "暂无已注册 Agent"
                          : agents.some((a) => a.local)
                            ? "本地面板（未绑定）"
                            : "未绑定（默认 Agent）"}
                      </option>
                      {agents
                        .filter((a) => !a.local)
                        .map((a) => (
                          <option key={a.id} value={String(a.id)}>
                            {a.name || "Agent #" + a.id}
                          </option>
                        ))}
                    </select>
                  </td>
                  <td>
                    <div className="actions">
                      <button className="action-btn danger" type="button" onClick={() => removeToken(t)}>
                        删除
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {/* 创建实例入口已移至「实例」页面（需 power 权限）。 */}

      {error && <div className="users-error">{error}</div>}
      {status && (
        <div className={"boot-script-status " + status.cls} style={{ marginTop: 8 }}>
          {status.msg}
        </div>
      )}

      {addTokenOpen && (
        <AddTokenModal
          onClose={() => setAddTokenOpen(false)}
          onAdded={() => {
            setStatus({ msg: "已添加账号", cls: "ok" });
            loadTokens();
          }}
        />
      )}
    </section>
  );
}
