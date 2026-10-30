// 重建任务列表：实例 # / 时间 / 旧 IP → 新 IP / 状态徽标 / 错误。
// tasks 为 null 表示加载中；error 非空表示加载失败。

import { fmtDateTime } from "../../utils";

function statusBadge(status) {
  const label =
    status === "running" ? "运行中" : status === "success" ? "成功" : status === "failed" ? "失败" : status || "未知";
  return <span className={"task-status " + (status || "")}>{label}</span>;
}

export default function TaskList({ tasks, error, onOpen }) {
  return (
    <section className="panel">
      <h2 className="panel-title">重建任务</h2>
      <div className="tasks-content">
        {tasks === null && <div className="tasks-empty">正在加载…</div>}
        {tasks !== null && error && (
          <div className="tasks-empty">加载失败：{error}</div>
        )}
        {tasks !== null && !error && tasks.length === 0 && (
          <div className="tasks-empty">暂无重建任务</div>
        )}
        {tasks !== null &&
          tasks.map((t) => (
            <div className="task-item" key={t.id} onClick={() => onOpen(t.id)}>
              <div className="task-item-main">
                <div className="task-item-title">实例 #{t.instance_id}</div>
                <div className="task-item-meta">
                  {fmtDateTime(t.started_at)} · 旧 IP {t.old_ip || "—"} → 新 IP {t.new_ip || "—"}
                </div>
              </div>
              {statusBadge(t.status)}
              {t.error && <div className="task-item-error">{t.error}</div>}
            </div>
          ))}
      </div>
    </section>
  );
}
