// 任务详情：运行中 2s 轮询（useTaskPolling）、日志反显并自动跟随滚动、
// 停止/继续跟随、返回列表，以及运行/成功/失败状态展示。

import { fmtDateTime } from "../../utils";
import useTaskPolling from "./useTaskPolling";

export default function TaskDetail({ taskId, onBack }) {
  const { task, follow, setFollow } = useTaskPolling(taskId);
  const lines = task && Array.isArray(task.log) ? task.log.slice().reverse() : [];

  return (
    <section className="panel">
      <div className="task-view-head">
        <span className="task-view-title">
          {task ? "实例 #" + task.instance_id + " · " + fmtDateTime(task.started_at) : "任务详情"}
        </span>
        {task && task.status === "running" && (
          <button
            className="btn btn-ghost task-follow-btn"
            type="button"
            onClick={() => setFollow((f) => !f)}
          >
            {follow ? "停止跟随" : "继续跟随"}
          </button>
        )}
        <button className="btn btn-ghost" type="button" onClick={onBack}>
          返回列表
        </button>
      </div>
      <pre className="task-log" ref={(el) => { if (el && follow) el.scrollTop = el.scrollHeight; }}>
        {lines.length === 0 ? (
          <span className="task-log-empty">
            {task && task.status === "running" ? "等待输出…" : "无输出"}
          </span>
        ) : (
          lines.map((line, i) => <span key={i}>{line + "\n"}</span>)
        )}
      </pre>
      <div className={"task-view-status" + (task ? (task.status === "failed" ? " error" : task.status === "success" ? " ok" : "") : "")}>
        {!task && <span>加载中…</span>}
        {task && task.status === "running" && (
          <>
            <span className="task-spinner" />
            任务运行中…
          </>
        )}
        {task && task.status === "failed" && (
          <span>任务失败：{task.error || "未知错误"}</span>
        )}
        {task && task.status === "success" && <span>任务成功</span>}
      </div>
    </section>
  );
}
