// 重建任务页：列表 / 详情切换编排。
// 列表（TaskList）：加载重建任务列表；详情（TaskDetail）：2s 轮询 + 日志反显。

import { useCallback, useEffect, useState } from "react";
import { listRebuildTasks } from "../api";
import TaskList from "../components/tasks/TaskList";
import TaskDetail from "../components/tasks/TaskDetail";

export default function Tasks() {
  const [tasks, setTasks] = useState(null); // null = 加载中
  const [error, setError] = useState("");
  const [view, setView] = useState(null); // 任务 id 或 null

  const loadList = useCallback(async () => {
    setTasks(null);
    setError("");
    try {
      const data = await listRebuildTasks();
      setTasks(Array.isArray(data.tasks) ? data.tasks : []);
    } catch (err) {
      setTasks([]);
      setError(err && err.message ? err.message : "未知错误");
    }
  }, []);

  useEffect(() => {
    loadList();
  }, [loadList]);

  if (view != null) {
    return <TaskDetail taskId={view} onBack={() => setView(null)} />;
  }
  return <TaskList tasks={tasks} error={error} onOpen={setView} />;
}
