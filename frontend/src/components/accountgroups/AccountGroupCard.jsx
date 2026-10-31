import { aggregateMemberStatus, groupMembers } from "../../utils/groupStatus";

// failover 色调 -> 复用现有 status-pill 系列样式类。
const TONE_PILL = {
  ok: "status-running",
  warn: "status-transition",
  danger: "status-danger",
  muted: "status-unknown",
};

// ---------- 账号组卡片 ----------
// 展示组名、成员数、成员账号标签，以及每个成员账号的替换状态
// （由 listFailovers() 的 failovers 按 group_id + token_id 聚合，取最严重状态）。
export default function AccountGroupCard({
  group,
  tokens,
  failovers,
  onManage,
  onRename,
  onDelete,
  onConfigureFailover,
  onOpenInstanceConfig,
}) {
  const members = groupMembers(group, tokens);
  const ic = group.instance_config;

  // 组配置摘要：已配置时显示 region/type，未配置时显示提示标签。
  const configLabel = ic
    ? ic.region + " / " + ic.type
    : "未填写目标开机配置";

  return (
    <article className="panel ag-card">
      <div className="ag-card-head">
        <div className="ag-card-heading">
          <h3 className="ag-card-title">{group.name || "（未命名组）"}</h3>
          <div className="ag-card-meta">成员账号 {members.length} 个</div>
        </div>
        <div className="actions">
          <button className="action-btn" type="button" onClick={() => onManage(group)}>
            管理成员
          </button>
          <button className="action-btn" type="button" onClick={() => onRename(group)}>
            改名
          </button>
          <button
            className="action-btn danger"
            type="button"
            onClick={() => onDelete(group)}
          >
            删除
          </button>
        </div>
      </div>

      {/* 目标开机配置状态标签 */}
      <div className="ag-card-config-row">
        <span
          className={
            "perm-chip" + (ic ? "" : " ag-config-missing")
          }
          title={ic ? "目标开机配置：" + configLabel : "未填写目标开机配置，替换时将报 error"}
        >
          {ic ? "目标开机配置：" + configLabel : configLabel}
        </span>
        <button
          className="action-btn ag-config-btn"
          type="button"
          onClick={() => onOpenInstanceConfig(group)}
        >
          目标开机配置
        </button>
      </div>

      {members.length === 0 ? (
        <div className="ag-card-empty">组内暂无账号，点击「管理成员」加入账号。</div>
      ) : (
        <ul className="ag-member-list">
          {members.map((m) => {
            const status = aggregateMemberStatus(failovers, group.id, m.id);
            return (
              <li className="ag-member-row" key={m.id}>
                <span className="ag-member-label">{m.label}</span>
                <span className="ag-member-id">#{m.id}</span>
                {!m.known && <span className="perm-chip">账号不存在</span>}
                <span
                  className={"status-pill " + (TONE_PILL[status.tone] || "status-unknown")}
                  title={status.count > 0 ? status.count + " 个实例已配置替换" : "尚无替换配置"}
                >
                  <span>{status.label}</span>
                  {status.count > 0 && (
                    <span className="ag-status-count">{status.count}</span>
                  )}
                </span>
                <button
                  className="action-btn"
                  type="button"
                  onClick={() => onConfigureFailover(group, { id: m.id, label: m.label })}
                >
                  自动替换
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </article>
  );
}
