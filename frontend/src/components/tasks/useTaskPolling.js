// 任务详情轮询 Hook：taskId 变化时立即拉取一次；若状态为 running，
// 每 TASK_POLL_INTERVAL_MS（2s）继续轮询，结束后停止。taskId 为 null 时清空。
// 同时维护日志自动跟随开关 follow（默认开启）。

import { useEffect, useState } from "react";
import { getRebuildTask } from "../../api";
import { TASK_POLL_INTERVAL_MS } from "../../constants";

export default function useTaskPolling(taskId) {
  const [task, setTask] = useState(null);
  const [follow, setFollow] = useState(true);

  useEffect(() => {
    if (taskId == null) {
      setTask(null);
      return;
    }
    let alive = true;
    let timer = null;
    async function poll() {
      try {
        const d = await getRebuildTask(taskId);
        if (!alive) return;
        setTask(d);
        if (d.status === "running") {
          timer = setTimeout(poll, TASK_POLL_INTERVAL_MS);
        }
      } catch (err) {
        if (alive) {
          setTask({ status: "failed", error: "加载失败：" + (err.message || ""), log: [] });
        }
      }
    }
    poll();
    return () => {
      alive = false;
      if (timer) clearTimeout(timer);
    };
  }, [taskId]);

  return { task, follow, setFollow };
}
