// 实例页：负责数据编排与页面状态（实例列表、加载、重建任务、模态框），
// 行级渲染与状态轮询已拆分到 components/instances/ 下。
import { useCallback, useEffect, useRef, useState } from "react";
import {
  apiFetch,
  instanceAction,
  listInstances,
  listRebuildTasks,
  rebuildInstance,
  refreshInstance,
} from "../api";
import { useAuth } from "../auth";
import { useUi } from "../ui";
import { usePolling } from "../hooks/usePolling";
import useInstancePolling from "../components/instances/useInstancePolling";
import InstanceTable from "../components/instances/InstanceTable";
import ActionModal from "../components/instances/ActionModal";
import RebuildModal from "../components/instances/RebuildModal";
import CreateInstanceModal from "../components/instances/CreateInstanceModal";
import InstanceScriptModal from "../components/InstanceScriptModal";

export default function Instances() {
  const { hasPerm } = useAuth();
  const { showBanner, hideBanner } = useUi();

  const [instances, setInstances] = useState([]);
  const [firstLoadDone, setFirstLoadDone] = useState(false);
  const [loading, setLoading] = useState(true);
  const [rebuildTaskRunning, setRebuildTaskRunning] = useState(false);
  const [actionModal, setActionModal] = useState(null); // { inst, action }
  const [rebuildModal, setRebuildModal] = useState(null); // inst
  const [scriptModal, setScriptModal] = useState(null); // inst
  const [customScriptIds, setCustomScriptIds] = useState(() => new Set());
  const [tokens, setTokens] = useState([]); // 已绑定账号（PAT），用于创建实例
  const [createOpen, setCreateOpen] = useState(false); // 创建实例模态框

  const instancesRef = useRef([]);
  instancesRef.current = instances;

  const findInstance = useCallback(
    (id) => instancesRef.current.find((i) => i.id === id),
    []
  );

  // 行状态（timer/sshReady/startedAt/…）+ 状态轮询 + SSH 探测，集中在 hook 中。
  const { getRowState, startPolling, stopPolling, resetRowStates, resetRowState, bump } =
    useInstancePolling({
      findInstance,
      showBanner,
    });

  // 哪些实例配置了自定义脚本（用于在行内标记）。
  const loadScriptStatus = useCallback(async () => {
    if (!hasPerm("script")) return;
    try {
      const data = await apiFetch("/api/instance-scripts");
      setCustomScriptIds(new Set(Array.isArray(data.configured) ? data.configured : []));
    } catch {
      // 标记加载失败可忽略：脚本按钮仍可点击。
    }
  }, [hasPerm]);

  // 已绑定账号（PAT）：供创建实例模态框选择目标账号。
  const loadTokens = useCallback(async () => {
    try {
      const data = await apiFetch("/api/account/tokens");
      setTokens(Array.isArray(data.tokens) ? data.tokens : []);
    } catch {
      setTokens([]);
    }
  }, []);

  // 加载实例列表：后端返回数据库缓存（首次无缓存时自动拉取并缓存）。
  // 实例的最新数据通过行级「刷新」按钮单独拉取（见 refreshRow）。
  const loadInstances = useCallback(async () => {
    setLoading(true);
    hideBanner();
    try {
      const data = await listInstances();
      setInstances(Array.isArray(data.instances) ? data.instances : []);
      setFirstLoadDone(true);
      resetRowStates();
      bump();
      loadScriptStatus();
    } catch (err) {
      showBanner("加载失败：" + (err.message || "未知错误"), false);
    } finally {
      setLoading(false);
    }
  }, [hideBanner, showBanner, loadScriptStatus, resetRowStates, bump]);

  // 重建任务运行中时轮询任务列表，以更新重建按钮可用状态。
  const checkRebuildTasks = useCallback(async () => {
    try {
      const data = await listRebuildTasks();
      const tasks = Array.isArray(data.tasks) ? data.tasks : [];
      setRebuildTaskRunning(tasks.some((t) => t.status === "running"));
    } catch {
      // 忽略瞬时错误。
    }
  }, []);

  // 初次加载 + 检查是否有重建任务运行中。
  useEffect(() => {
    loadInstances();
    loadTokens();
    checkRebuildTasks();
    return () => {
      resetRowStates();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // rebuildTaskRunning 为 true 时每 5s 轮询重建任务，全部结束后自动停止。
  usePolling(checkRebuildTasks, 5000, rebuildTaskRunning);

  // ---------- 行级刷新（仅该实例通过 API 获取最新） ----------
  const refreshRow = async (inst) => {
    hideBanner();
    const tokenId = inst && inst.token_id != null ? inst.token_id : "";
    try {
      const data = await refreshInstance(inst.id, tokenId);
      if (!data || data.id == null) throw new Error("未返回实例数据");
      setInstances((prev) => prev.map((i) => (i.id === inst.id ? data : i)));
      resetRowState(inst.id);
      bump();
    } catch (err) {
      showBanner("刷新失败：" + (err.message || "未知错误"), false);
    }
  };

  // ---------- 操作与轮询 ----------
  const performAction = async (id, action, choice) => {
    hideBanner();
    const st = getRowState(id);
    const viaAgent = !!(choice && choice.viaAgent);
    const agentId = choice && choice.agentId ? String(choice.agentId) : "";
    st.action = action;
    st.sshTimeout = false;
    st.sawTransition = false;
    st.failMsg = "";
    const optimistic =
      action === "boot" ? "booting" : action === "shutdown" ? "shutting_down" : "rebooting";
    st.lastStatus = optimistic;
    st.sshReady = false;
    st.sshError = "";
    st.sshProbeDone = false;
    bump();

    try {
      const inst = findInstance(id);
      const tokenId = inst && inst.token_id != null ? inst.token_id : "";
      const res = await instanceAction(id, action, { tokenId, viaAgent, agentId });
      if (viaAgent) {
        if (!res || res.command_id == null) throw new Error("未返回命令 ID");
        st.lastStatus = null;
        stopPolling(id);
        bump();
        showBanner("已下发至 Agent（命令 #" + res.command_id + "），可在 Agent 面板查看进度", false);
        return;
      }
      if (!res || res.ok !== true) throw new Error("操作未成功");
    } catch (err) {
      showBanner(err.message || "操作失败", false);
      st.lastStatus = null;
      stopPolling(id);
      bump();
      return;
    }
    st.startedAt = Date.now();
    startPolling(id);
  };

  const confirmRebuild = async ({ script, ip, viaAgent, agentId }) => {
    hideBanner();
    const inst = rebuildModal;
    const payload = { script };
    if (ip) payload.reserved_ip = ip;
    if (!viaAgent && inst && inst.token_id != null) payload.token_id = inst.token_id;
    const res = await rebuildInstance(inst.id, payload, viaAgent, agentId);
    if (viaAgent) {
      if (!res || res.command_id == null) throw new Error("未返回命令 ID");
      showBanner("已下发至 Agent（命令 #" + res.command_id + "），可在 Agent 面板查看进度", false);
      return;
    }
    if (!res || !res.task_id) throw new Error("未返回任务 ID");
    setRebuildTaskRunning(true);
    showBanner("重建任务已提交", false);
    loadInstances();
  };

  const hasInstances = instances.length > 0;

  return (
    <>
      {hasPerm("power") && (
        <div className="agents-section-head" style={{ marginBottom: 12 }}>
          <h3 className="agents-section-title">实例</h3>
          <button
            className="btn btn-primary"
            type="button"
            onClick={() => setCreateOpen(true)}
          >
            创建实例
          </button>
        </div>
      )}
      {loading && !firstLoadDone && <div className="page-status">正在加载实例…</div>}
      {firstLoadDone && !hasInstances && <div className="empty-state">账号下没有实例</div>}
      {firstLoadDone && hasInstances && (
        <InstanceTable
          instances={instances}
          getRowState={getRowState}
          rebuildTaskRunning={rebuildTaskRunning}
          customScriptIds={customScriptIds}
          hasPerm={hasPerm}
          onAction={(inst, action) => setActionModal({ inst, action })}
          onRebuild={(inst) => setRebuildModal(inst)}
          onScript={(inst) => setScriptModal(inst)}
          onRefresh={refreshRow}
        />
      )}

      {actionModal && (
        <ActionModal
          inst={actionModal.inst}
          action={actionModal.action}
          onClose={() => setActionModal(null)}
          onConfirm={(choice) =>
            performAction(actionModal.inst.id, actionModal.action, choice)
          }
        />
      )}
      {rebuildModal && (
        <RebuildModal
          inst={rebuildModal}
          onClose={() => setRebuildModal(null)}
          onConfirm={confirmRebuild}
        />
      )}
      {scriptModal && (
        <InstanceScriptModal
          inst={scriptModal}
          onClose={() => setScriptModal(null)}
          onChanged={loadScriptStatus}
        />
      )}
      {createOpen && (
        <CreateInstanceModal
          tokens={tokens}
          onClose={() => setCreateOpen(false)}
          onCreated={() => {
            loadInstances();
            showBanner("实例已创建", false);
          }}
        />
      )}
    </>
  );
}
