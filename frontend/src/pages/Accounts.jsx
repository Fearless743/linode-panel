import { useCallback, useEffect, useState } from "react";
import { apiFetch, listAccountAgents, updateAccountToken, listDual, listInstances } from "../api";
import AddTokenModal from "../components/accounts/AddTokenModal";
import DualModal from "../components/accounts/DualModal";
import EditFormModal from "../components/ui/EditFormModal";
import AccountHealthCell from "../components/accounts/AccountHealthCell";

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
  const [editToken, setEditToken] = useState(null); // token object to edit, null = closed
  const [dualToken, setDualToken] = useState(null); // 双机轮询设置弹窗的账号
  const [dualList, setDualList] = useState([]);
  const [allInstances, setAllInstances] = useState([]);

  const loadDual = useCallback(async () => {
    try {
      const data = await listDual();
      setDualList(Array.isArray(data.dual) ? data.dual : []);
    } catch {
      setDualList([]);
    }
  }, []);

  const loadAllInstances = useCallback(async () => {
    try {
      const data = await listInstances();
      setAllInstances(Array.isArray(data.instances) ? data.instances : []);
    } catch {
      setAllInstances([]);
    }
  }, []);

  function dualOf(tokenId) {
    return dualList.find((d) => d.token_id === tokenId) || null;
  }

  function instancesOf(tokenId) {
    return allInstances.filter((i) => i.token_id === tokenId && !i.missing);
  }

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
    loadDual();
    loadAllInstances();
  }, [loadTokens, loadDual, loadAllInstances]);

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

  // 编辑账号：弹窗提交后本地更新，避免整表刷新闪烁。
  async function handleEditSubmit(editToken, values) {
    const agentId = values.agent_id === "" ? 0 : Number(values.agent_id);
    await updateAccountToken(editToken.id, {
      label: values.label,
      agent_id: agentId,
    });
    setTokens((prev) =>
      (prev || []).map((x) =>
        x.id === editToken.id
          ? { ...x, label: values.label, agent_id: agentId, agent_name: "" }
          : x
      )
    );
    setStatus({ msg: "账号已更新", cls: "ok" });
    setEditToken(null);
  }

  // 编辑弹窗字段描述。
  function editFieldsFor(t) {
    const placeholder =
      agents.length === 0
        ? "暂无已注册 Agent"
        : agents.some((a) => a.local)
          ? "本地面板（未绑定）"
          : "未绑定（默认 Agent）";
    return [
      {
        key: "label",
        label: "名称",
        type: "text",
        placeholder: "名称（如 生产账号）",
      },
      {
        key: "agent_id",
        label: "执行 Agent",
        type: "select",
        required: false, // 空值 = 解绑 / 默认 Agent，合法
        options: [
          { value: "", label: placeholder },
          ...agents
            .filter((a) => !a.local)
            .map((a) => ({ value: String(a.id), label: a.name || "Agent #" + a.id })),
        ],
      },
    ];
  }

  function editInitialValuesFor(t) {
    const agentBound =
      t.agent_id > 0 && agents.some((a) => a.id === t.agent_id && !a.local);
    return {
      label: t.label || "",
      agent_id: agentBound ? String(t.agent_id) : "",
    };
  }

  // 行内显示当前绑定 Agent 名称。
  function boundAgentLabel(t) {
    if (!t.agent_id) return null;
    const a = agents.find((x) => x.id === t.agent_id);
    return a ? a.name || "Agent #" + a.id : "Agent #" + t.agent_id;
  }

  return (
    <section className="panel">
      <h2 className="panel-title">账号管理</h2>
      <p className="boot-script-hint">
        每个账号对应一个 Linode PAT；绑定后可用于聚合实例、开关机与创建实例。将账号绑定到
        某个 Agent 后，该账号的开关机、重建、删除等操作会自动经该 Agent 执行（未绑定则面板直发）。
      </p>

      {/* 账号列表 */}
      <div className="agents-section">
        <div className="agents-section-head">
          <h3 className="agents-section-title">已绑定账号</h3>
          <div className="actions">
            <button className="btn btn-ghost" type="button" onClick={loadTokens}>
              重新检测
            </button>
            <button
              className="btn btn-primary"
              type="button"
              onClick={() => setAddTokenOpen(true)}
            >
              绑定新账号
            </button>
          </div>
        </div>
        {tokens === null && <div className="users-empty">正在加载…</div>}
        {tokens !== null && tokens.length === 0 && <div className="users-empty">尚未绑定账号</div>}
        {tokens !== null && tokens.length > 0 && (
          <table className="users-table">
            <thead>
              <tr>
                <th>名称</th>
                <th>Token</th>
                <th>余额 / 状态</th>
                <th>双机轮询</th>
                <th>执行 Agent</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {tokens.map((t) => {
                const d = dualOf(t.id);
                return (
                <tr key={t.id}>
                  <td>{t.label || "（未命名）"}</td>
                  <td>{t.masked || ""}</td>
                  <td>
                    <AccountHealthCell token={t} />
                  </td>
                  <td>
                    {d && d.enabled ? (
                      <span title={`使用中 #${d.active_instance_id} · 备用 #${d.standby_instance_id || "—"} · ${d.last_status || d.state || ""}`}>
                        开启（A #{d.active_instance_id}{d.standby_instance_id ? ` / 备 #${d.standby_instance_id}` : ""}）
                      </span>
                    ) : (
                      <span className="agent-unbound">关闭</span>
                    )}
                    <div className="actions" style={{ marginTop: 4 }}>
                      <button className="action-btn" type="button" onClick={() => setDualToken(t)}>
                        双机设置
                      </button>
                    </div>
                  </td>
                  <td>
                    {boundAgentLabel(t) ? (
                      <span>{boundAgentLabel(t)}</span>
                    ) : (
                      <span className="agent-unbound">未绑定</span>
                    )}
                  </td>
                  <td>
                    <div className="actions">
                      <button className="action-btn" type="button" onClick={() => setEditToken(t)}>
                        编辑
                      </button>
                      <button className="action-btn danger" type="button" onClick={() => removeToken(t)}>
                        删除
                      </button>
                    </div>
                  </td>
                </tr>
                );
              })}
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

      {editToken && (
        <EditFormModal
          title={"编辑账号 · " + (editToken.label || "#" + editToken.id)}
          description="修改账号名称与绑定执行 Agent"
          fields={editFieldsFor(editToken)}
          initialValues={editInitialValuesFor(editToken)}
          busyLabel="保存中…"
          onSubmit={(values) => handleEditSubmit(editToken, values)}
          onClose={() => setEditToken(null)}
        />
      )}

      {dualToken && (
        <DualModal
          token={dualToken}
          dual={dualOf(dualToken.id)}
          instances={instancesOf(dualToken.id)}
          onClose={() => setDualToken(null)}
          onSaved={() => {
            loadDual();
            loadAllInstances();
          }}
        />
      )}
    </section>
  );
}
