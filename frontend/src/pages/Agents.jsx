// Agent 页：数据加载 + 5s 轮询编排（usePolling），UI 拆分到 components/agents/。
import { useCallback, useState } from "react";
import {
  listAgents,
  createAgent,
  deleteAgent,
  renameAgent,
  setRegcode,
  genRegcode,
  listAgentCommands,
} from "../api";
import { useUi } from "../ui";
import { AGENT_POLL_INTERVAL_MS } from "../constants";
import { usePolling } from "../hooks/usePolling";
import AgentList from "../components/agents/AgentList";
import RegCodeManager from "../components/agents/RegCodeManager";
import CommandList from "../components/agents/CommandList";
import AgentCreatedModal from "../components/agents/AgentCreatedModal";

export default function Agents() {
  const { showBanner } = useUi();
  const [data, setData] = useState(null); // { agents, commands, results }
  const [error, setError] = useState("");
  const [created, setCreated] = useState(null);

  const load = useCallback(async () => {
    try {
      const agentsRes = await listAgents();
      const agents = Array.isArray(agentsRes.agents) ? agentsRes.agents : [];
      let commands = [];
      let results = [];
      try {
        const c = await listAgentCommands(20);
        commands = Array.isArray(c.commands) ? c.commands : [];
        results = Array.isArray(c.results) ? c.results : [];
      } catch {
        // 命令记录接口暂不可用时，仍展示 agent 列表与注册码管理。
      }
      setData({ agents, commands, results });
      setError("");
    } catch (err) {
      setData(null);
      setError(err && err.message ? err.message : "未知错误");
    }
  }, []);

  // 5s 轮询：enabled 恒 true，立即执行一次并循环。
  usePolling(load, AGENT_POLL_INTERVAL_MS, true);

  // 创建 Agent：成功返回 true（清空输入），失败返回 false。
  async function handleCreate(name) {
    if (!name.trim()) {
      showBanner("请输入 Agent 名称", false);
      return false;
    }
    try {
      const res = await createAgent(name.trim());
      setCreated(res);
      await load();
      return true;
    } catch (err) {
      showBanner(err && err.message ? err.message : "创建失败", false);
      return false;
    }
  }

  async function handleDelete(a) {
    try {
      await deleteAgent(a.id);
      await load();
    } catch (err) {
      showBanner(err && err.message ? err.message : "删除失败", false);
    }
  }

  // 重命名本地面板 Agent：成功返回 true（关闭弹窗），失败返回 false。
  async function handleRename(a, name) {
    try {
      await renameAgent(a.id, name.trim());
      showBanner("已重命名为「" + name.trim() + "」", false);
      await load();
      return true;
    } catch (err) {
      showBanner(err && err.message ? err.message : "重命名失败", false);
      return false;
    }
  }

  // 设置注册码：成功返回 true（清空输入），失败返回 false。
  async function handleSetRegcode(code) {
    if (!code.trim()) {
      showBanner("请输入注册码", false);
      return false;
    }
    try {
      await setRegcode(code.trim());
      showBanner("注册码已设置", false);
      return true;
    } catch (err) {
      showBanner(err && err.message ? err.message : "设置失败", false);
      return false;
    }
  }

  async function handleGenRegcode() {
    try {
      const res = await genRegcode();
      showBanner(
        res && res.registration_code ? "新注册码：" + res.registration_code : "已生成新注册码",
        false
      );
    } catch (err) {
      showBanner(err && err.message ? err.message : "生成失败", false);
    }
  }

  const agents = data ? data.agents : [];
  const commands = data ? data.commands : [];
  const results = data ? data.results : [];

  return (
    <section className="panel">
      <h2 className="panel-title">Agent 管理</h2>
      {error && <div className="agents-error">加载失败：{error}</div>}
      {data === null && !error && <div className="agents-empty">正在加载…</div>}

      <AgentList
        agents={agents}
        onCreate={handleCreate}
        onDelete={handleDelete}
        onRename={handleRename}
      />
      <RegCodeManager onSetRegcode={handleSetRegcode} onGenRegcode={handleGenRegcode} />
      <CommandList commands={commands} results={results} />

      {created && <AgentCreatedModal res={created} onClose={() => setCreated(null)} />}
    </section>
  );
}
