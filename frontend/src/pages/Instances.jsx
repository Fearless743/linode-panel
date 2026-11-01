// 实例页：负责数据编排与页面状态（实例列表、加载、重建任务、模态框），
// 行级渲染与状态轮询已拆分到 components/instances/ 下。
import { useCallback, useEffect, useRef, useState } from "react";
import {
  apiFetch,
  deleteInstance,
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
import InstanceEditModal from "../components/instances/InstanceEditModal";
import ConfirmDialog from "../components/ui/ConfirmDialog";

export default function Instances() {
  const { hasPerm } = useAuth();
  const { showBanner, hideBanner } = useUi();

  const [instances, setInstances] = useState([]);
  const [firstLoadDone, setFirstLoadDone] = useState(false);
  const [loading, setLoading] = useState(true);
  const [rebuildTaskRunning, setRebuildTaskRunning] = useState(false);
  const [actionModal, setActionModal] = useState(null); // { inst, action }
  const [rebuildModal, setRebuildModal] = useState(null); // inst
  const [editModal, setEditModal] = useState(null); // inst
  const [deleteModal, setDeleteModal] = useState(null); // inst
  const [customScriptIds, setCustomScriptIds] = useState(() => new Set());
  // 已开启「被墙后自动换 IP」的实例 id 集合（用于行内标记）。
  const [autoRebuildIds, setAutoRebuildIds] = useState(() => new Set());
  const [tokens, setTokens] = useState([]); // 已绑定账号（PAT），用于创建实例
  const [createOpen, setCreateOpen] = useState(false); // 创建实例模态框
  // 正在按账号批量刷新中的分组 key（token_id，0 表示未绑定账号）。
  const [refreshingKey, setRefreshingKey] = useState(null);

  const instancesRef = useRef([]);
  instancesRef.current = instances;

  const findInstance = useCallback(
    (id) => instancesRef.current.find((i) => i.id === id),
    []
  );

  // 实例在上游已不存在（404）时从列表移除，避免「卡实例」一直显示。
  const removeInstance = useCallback((id) => {
    setInstances((prev) => prev.filter((i) => i.id !== id));
  }, []);

  // 行状态（timer/sshReady/startedAt/…）+ 状态轮询 + SSH 探测，集中在 hook 中。
  const { getRowState, startPolling, stopPolling, resetRowStates, resetRowState, bump } =
    useInstancePolling({
      findInstance,
      showBanner,
      onInstanceMissing: removeInstance,
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

  // 哪些实例开启了「被墙后自动换 IP」（用于行内标记）。
  const loadAutoRebuildStatus = useCallback(async () => {
    if (!hasPerm("rebuild")) return;
    try {
      const data = await apiFetch("/api/auto-rebuild");
      const items = Array.isArray(data.auto_rebuild) ? data.auto_rebuild : [];
      setAutoRebuildIds(new Set(items.filter((i) => i.enabled).map((i) => i.instance_id)));
    } catch {
      // 标记加载失败可忽略：自动换 IP 按钮仍可点击。
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

  // 账号（token id）绑定的执行 Agent 名称；空串表示未绑定（面板直发）。
  // 供操作/重建模态框显示执行通道，并用于删除确认提示。
  const boundAgentNameOf = useCallback(
    (tokenId) => {
      const t = tokens.find((x) => x.id === tokenId);
      if (!t || !(t.agent_id > 0)) return "";
      return t.agent_name || "Agent #" + t.agent_id;
    },
    [tokens]
  );

  // 加载实例列表：后端返回数据库缓存（首次无缓存时自动拉取并缓存）。
  // 实例的最新数据通过账号分组行的「刷新」按钮批量拉取（见 refreshGroup）。
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
      loadAutoRebuildStatus();
    } catch (err) {
      showBanner("加载失败：" + (err.message || "未知错误"), false);
    } finally {
      setLoading(false);
    }
  }, [hideBanner, showBanner, loadScriptStatus, loadAutoRebuildStatus, resetRowStates, bump]);

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

  // ---------- 账号级刷新（仅该账号下的实例通过 API 获取最新） ----------
  const refreshGroup = async (group) => {
    hideBanner();
    const list = Array.isArray(group.instances) ? group.instances : [];
    if (list.length === 0 || refreshingKey !== null) return;
    setRefreshingKey(group.key);
    const results = await Promise.allSettled(
      list.map((inst) => refreshInstance(inst.id, inst.token_id != null ? inst.token_id : ""))
    );

    const refreshed = [];
    const missingIds = [];
    const failed = [];
    results.forEach((res, idx) => {
      const inst = list[idx];
      if (res.status === "rejected") {
        const err = res.reason || {};
        if (err.status === 404) {
          missingIds.push(inst.id);
        } else {
          failed.push((inst.label || "#" + inst.id) + "：" + (err.message || "未知错误"));
        }
        return;
      }
      const data = res.value;
      if (!data || data.id == null) {
        failed.push((inst.label || "#" + inst.id) + "：未返回实例数据");
        return;
      }
      refreshed.push(data);
      resetRowState(inst.id);
    });

    if (refreshed.length > 0 || missingIds.length > 0) {
      setInstances((prev) => {
        const byId = new Map(refreshed.map((i) => [i.id, i]));
        return prev
          .filter((i) => !missingIds.includes(i.id))
          .map((i) => byId.get(i.id) || i);
      });
      bump();
    }
    setRefreshingKey(null);

    const notes = [];
    if (missingIds.length > 0) {
      notes.push("有 " + missingIds.length + " 个实例在 Linode 已不存在，已从列表移除");
    }
    if (failed.length > 0) {
      notes.push("刷新失败：" + failed.join("；"));
    }
    if (notes.length === 0 && refreshed.length > 0) {
      notes.push("账号「" + group.label + "」的 " + refreshed.length + " 个实例已刷新");
    }
    if (notes.length > 0) showBanner(notes.join("；"), false);
  };

  // ---------- 操作与轮询 ----------
  const performAction = async (id, action, choice) => {
    hideBanner();
    const st = getRowState(id);
    // undefined = 跟随账号绑定（后端决定执行通道）。
    const viaAgent = choice ? choice.viaAgent : undefined;
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
      // 后端经 Agent 下发时返回 command_id（auto 模式命中绑定时同样如此）。
      if (res && res.command_id != null) {
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

  const confirmRebuild = async ({ script, root_password, viaAgent, agentId }) => {
    hideBanner();
    const inst = rebuildModal;
    const payload = { script };
    if (root_password) payload.root_password = root_password;
    // 目标账号始终随请求下发：直发用它调用 API，经 Agent 时用它解析 PAT。
    if (inst && inst.token_id != null) payload.token_id = inst.token_id;
    const res = await rebuildInstance(inst.id, payload, viaAgent, agentId);
    // 经 Agent 下发（含 auto 模式命中绑定）返回 command_id。
    if (res && res.command_id != null) {
      showBanner("已下发至 Agent（命令 #" + res.command_id + "），可在 Agent 面板查看进度", false);
      return;
    }
    if (!res || !res.task_id) throw new Error("未返回任务 ID");
    setRebuildTaskRunning(true);
    showBanner("重建任务已提交", false);
    loadInstances();
  };

  // ---------- 删除实例 ----------
  // 确认后调用 API 永久删除实例；执行通道跟随账号绑定。
  const confirmDelete = async () => {
    const inst = deleteModal;
    if (!inst) return;
    hideBanner();
    const tokenId = inst.token_id != null ? inst.token_id : "";
    try {
      const res = await deleteInstance(inst.id, tokenId);
      // 经 Agent 下发时删除是异步的，不立即移除行（后续刷新会因 404 自动移除）。
      if (res && res.command_id != null) {
        showBanner("删除命令已下发至 Agent（命令 #" + res.command_id + "）", false);
      } else {
        stopPolling(inst.id);
        resetRowState(inst.id);
        removeInstance(inst.id);
        bump();
        showBanner("实例已删除", false);
      }
    } catch (err) {
      showBanner("删除失败：" + (err.message || "未知错误"), false);
    } finally {
      setDeleteModal(null);
    }
  };

  const hasInstances = instances.length > 0;
  // 删除确认里展示实际执行通道（跟随账号绑定）。
  const deleteAgentName = deleteModal ? boundAgentNameOf(deleteModal.token_id) : "";

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
          autoRebuildIds={autoRebuildIds}
          hasPerm={hasPerm}
          agentNameForToken={boundAgentNameOf}
          onAction={(inst, action) => setActionModal({ inst, action })}
          onRebuild={(inst) => setRebuildModal(inst)}
          onEdit={(inst) => setEditModal(inst)}
          onRefreshGroup={refreshGroup}
          refreshingKey={refreshingKey}
          onDelete={(inst) => setDeleteModal(inst)}
        />
      )}

      {actionModal && (
        <ActionModal
          inst={actionModal.inst}
          action={actionModal.action}
          boundAgentName={boundAgentNameOf(actionModal.inst.token_id)}
          onClose={() => setActionModal(null)}
          onConfirm={(choice) =>
            performAction(actionModal.inst.id, actionModal.action, choice)
          }
        />
      )}
      {rebuildModal && (
        <RebuildModal
          inst={rebuildModal}
          boundAgentName={boundAgentNameOf(rebuildModal.token_id)}
          onClose={() => setRebuildModal(null)}
          onConfirm={confirmRebuild}
        />
      )}
      {editModal && (
        <InstanceEditModal
          inst={editModal}
          canScript={hasPerm("script")}
          canAutoRebuild={hasPerm("rebuild")}
          onClose={() => setEditModal(null)}
          onScriptChanged={loadScriptStatus}
          onAutoRebuildChanged={loadAutoRebuildStatus}
        />
      )}
      {deleteModal && (
        <ConfirmDialog
          title="删除实例"
          message={
            "确定删除实例 " +
            (deleteModal.label || "#" + deleteModal.id) +
            " 吗？该操作不可恢复，实例及其磁盘将被永久删除。" +
            (deleteAgentName
              ? "将通过 Agent「" + deleteAgentName + "」执行。"
              : "将通过面板直发执行。")
          }
          confirmText="删除"
          danger
          onConfirm={confirmDelete}
          onCancel={() => setDeleteModal(null)}
        />
      )}
      {createOpen && (
        <CreateInstanceModal
          tokens={tokens}
          onClose={() => setCreateOpen(false)}
          onCreated={() => {
            loadInstances();
            showBanner("实例已创建；开机脚本将在实例启动后自动执行", false);
          }}
        />
      )}
    </>
  );
}
