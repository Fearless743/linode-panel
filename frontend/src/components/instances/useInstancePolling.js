// 实例行状态管理与状态轮询 Hook。
//
// 从 Instances.jsx 抽出的行级状态（timer/action/startedAt/sshReady/…）与
// 3s 状态轮询 + SSH 端口探测（5min 超时）逻辑，集中在此，页面不再关心。
// 行状态以 useRef Map 保存，避免 setState 频繁触发渲染。
//
// findInstance / showBanner 由调用方传入且引用稳定（useCallback），
// 因此可直接作为依赖，无需再经 ref 转发。

import { useCallback, useEffect, useRef, useState } from "react";
import { getInstanceStatus } from "../../api";
import { POLL_INTERVAL_MS, SSH_TIMEOUT_MS, TRANSITION_STATES } from "../../constants";

// 是否为过渡态（操作进行中，操作按钮应禁用）。
export function isTransition(status) {
  return TRANSITION_STATES.has(status);
}

function freshRowState() {
  return {
    timer: null,
    action: null,
    startedAt: 0,
    lastStatus: null,
    sshReady: false,
    sshError: "",
    sshProbeDone: false,
    sshTimeout: false,
    sawTransition: false,
    statusData: null,
    failMsg: "",
  };
}

function stopTimer(st) {
  if (st && st.timer) {
    clearInterval(st.timer);
    st.timer = null;
  }
}

export default function useInstancePolling({ findInstance, showBanner }) {
  const rowStates = useRef(new Map());
  const [, setTick] = useState(0);

  const bump = useCallback(() => setTick((t) => t + 1), []);

  const getRowState = useCallback((id) => {
    let st = rowStates.current.get(id);
    if (!st) {
      st = freshRowState();
      rowStates.current.set(id, st);
    }
    return st;
  }, []);

  // 单次状态查询：更新行状态，并处理「启动失败 / SSH 就绪 / SSH 超时」终态。
  const pollOnce = useCallback(
    async (id) => {
      const st = getRowState(id);
      let data;
      try {
        const inst = findInstance(id);
        const tokenId = inst && inst.token_id != null ? inst.token_id : "";
        data = await getInstanceStatus(id, tokenId);
      } catch (err) {
        showBanner(err.message || "状态查询失败", false);
        return;
      }
      const status = data.status;
      st.lastStatus = status;
      st.sshReady = data.ssh_ready === true;
      st.sshError = data.ssh_error || "";
      st.statusData = data;
      if (isTransition(status)) st.sawTransition = true;

      // 开机后重新回到 offline：若曾进入过渡态则判定为启动失败。
      if (status === "offline" && st.startedAt > 0 && st.sawTransition) {
        stopTimer(st);
        st.startedAt = 0;
        if (st.action === "shutdown") {
          st.failMsg = "";
          showBanner("实例已关机", false);
        } else {
          st.failMsg = "启动失败";
        }
        bump();
        return;
      }
      bump();

      if (status === "running") {
        if (st.sshReady) {
          st.sshProbeDone = true;
          st.sshTimeout = false;
          stopTimer(st);
          st.startedAt = 0;
          bump();
        } else if (Date.now() - st.startedAt >= SSH_TIMEOUT_MS) {
          st.sshProbeDone = true;
          st.sshTimeout = true;
          st.sshError = "";
          stopTimer(st);
          st.startedAt = 0;
          bump();
        }
      } else if (st.startedAt > 0 && Date.now() - st.startedAt >= SSH_TIMEOUT_MS) {
        stopTimer(st);
        st.startedAt = 0;
      }
    },
    [getRowState, bump, findInstance, showBanner]
  );

  // 开始轮询：立即查询一次，随后每 POLL_INTERVAL_MS 查询一次。
  const startPolling = useCallback(
    (id) => {
      const st = getRowState(id);
      stopTimer(st);
      pollOnce(id);
      st.timer = setInterval(() => pollOnce(id), POLL_INTERVAL_MS);
    },
    [getRowState, pollOnce]
  );

  // 停止某个实例的轮询。
  const stopPolling = useCallback(
    (id) => {
      stopTimer(getRowState(id));
    },
    [getRowState]
  );

  // 重置所有行状态（手动刷新时）。
  const resetRowStates = useCallback(() => {
    rowStates.current.forEach((st) => {
      stopTimer(st);
      Object.assign(st, freshRowState());
    });
  }, []);

  // 重置单个实例的行状态（行级刷新时，停掉该行轮询并清除其探测结果）。
  const resetRowState = useCallback(
    (id) => {
      const st = getRowState(id);
      stopTimer(st);
      Object.assign(st, freshRowState());
    },
    [getRowState]
  );

  // 卸载时清理所有定时器。
  useEffect(() => {
    const map = rowStates.current;
    return () => {
      map.forEach((st) => stopTimer(st));
    };
  }, []);

  return { getRowState, startPolling, stopPolling, resetRowStates, resetRowState, bump };
}
