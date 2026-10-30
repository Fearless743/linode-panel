// 命令记录：最近 20 条命令，结果可展开，展示进度 / 消息。
import { useState } from "react";
import { fmtDateTime } from "../../utils";
import { COMMAND_TYPE_LABELS, COMMAND_STATUS_LABELS } from "../../constants";

function CommandItem({ cmd, results }) {
  const [open, setOpen] = useState(false);
  const cmdResults = results.filter((r) => r.command_id === cmd.id);
  const status = cmd.status;
  let badgeCls = "";
  if (status === "in_flight") badgeCls = "running";
  else if (status === "completed") {
    badgeCls = cmdResults.some((r) => r.ok) ? "success" : "failed";
  }
  return (
    <div className="cmd-item">
      <div className="cmd-item-head" onClick={() => cmdResults.length > 0 && setOpen((o) => !o)}>
        <div className="cmd-item-main">
          <div className="cmd-item-title">
            #{cmd.id}{" "}
            {COMMAND_TYPE_LABELS[cmd.type] ||
              cmd.type ||
              (status === "completed" ? "已完成命令" : "命令")}
          </div>
          <div className="cmd-item-meta">
            {[cmd.agent_id != null ? "Agent #" + cmd.agent_id : "", COMMAND_STATUS_LABELS[status] || status, fmtDateTime(cmd.created_at)]
              .filter(Boolean)
              .join(" · ")}
          </div>
        </div>
        <span className={"task-status " + badgeCls}>{COMMAND_STATUS_LABELS[status] || status}</span>
        {cmdResults.length > 0 && (
          <span className="agents-muted cmd-expand-hint">▸ 详情</span>
        )}
      </div>
      {open && cmdResults.length > 0 && (
        <div className="cmd-results">
          {cmdResults.map((r, i) => (
            <div className="cmd-result" key={i}>
              {r.ok ? (
                <div className="cmd-result-ok">成功</div>
              ) : (
                <div className="cmd-result-err">失败：{r.error || "未知错误"}</div>
              )}
              <div className="cmd-result-meta">{fmtDateTime(r.at)}</div>
              {r.payload && r.payload.progress != null && (
                <div className="cmd-progress">进度：{String(r.payload.progress)}</div>
              )}
              {r.payload && r.payload.message && (
                <div className="cmd-progress">{String(r.payload.message)}</div>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

export default function CommandList({ commands, results }) {
  // 补充：仅存在于 results、但不在 commands 里的已完成命令（无头部记录）。
  const seen = new Set(commands.map((c) => c.id));
  const extra = [];
  results.forEach((r) => {
    if (!seen.has(r.command_id)) {
      extra.push({
        id: r.command_id,
        agent_id: r.agent_id,
        type: r.type || "",
        status: "completed",
        created_at: r.at,
      });
    }
  });
  const all = commands.concat(extra);

  return (
    <div className="agents-section">
      <h3 className="agents-section-title">命令记录（最近 20 条）</h3>
      {all.length === 0 ? (
        <div className="agents-empty">暂无命令</div>
      ) : (
        <div className="cmd-list">
          {all.map((cmd) => (
            <CommandItem key={cmd.id + "-" + cmd.status} cmd={cmd} results={results} />
          ))}
        </div>
      )}
    </div>
  );
}
