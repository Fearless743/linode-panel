// 实例表格：表头 + 按账号分组的实例行列表，行内包含操作按钮与 SSH 卡片。
import { Fragment } from "react";
import InstanceRow from "./InstanceRow";

// 按账号（token）把实例分组，保持实例原有顺序；同账号的实例连续展示，
// 每组以「账号：xxx」标题行分隔。
function groupByAccount(instances) {
  const groups = [];
  const byKey = new Map();
  for (const inst of instances) {
    const key = inst.token_id != null ? inst.token_id : 0;
    let group = byKey.get(key);
    if (!group) {
      group = {
        key,
        label: inst.token_label || "未绑定账号",
        instances: [],
      };
      byKey.set(key, group);
      groups.push(group);
    }
    group.instances.push(inst);
  }
  return groups;
}

export default function InstanceTable({
  instances,
  getRowState,
  rebuildRunningIds,
  customScriptIds,
  autoRebuildIds,
  hasPerm,
  agentNameForToken,
  onAction,
  onRebuild,
  onEdit,
  onRefreshGroup,
  onDelete,
  refreshingKey,
}) {
  const groups = groupByAccount(instances);
  return (
    <div className="table-wrap">
      <table className="instances">
        <thead>
          <tr>
            <th>名称</th>
            <th>状态</th>
            <th>地区 / 型号</th>
            <th>公网 IP</th>
            <th>操作</th>
          </tr>
        </thead>
        <tbody>
          {groups.map((group) => (
            <Fragment key={group.key}>
              <tr className="account-group-row">
                <td colSpan={5}>
                  <div className="account-group-head">
                    <span>
                      <span className="account-group-label">账号：{group.label}</span>
                      <span className="account-group-count">
                        {group.instances.length} 个实例
                      </span>
                      {agentNameForToken && agentNameForToken(group.key) && (
                        <span className="account-group-count">
                          · Agent：{agentNameForToken(group.key)}
                        </span>
                      )}
                    </span>
                    <button
                      className="action-btn"
                      type="button"
                      title="通过 API 刷新该账号下的全部实例"
                      disabled={refreshingKey !== null}
                      onClick={() => onRefreshGroup(group)}
                    >
                      {refreshingKey === group.key ? "刷新中…" : "刷新"}
                    </button>
                  </div>
                </td>
              </tr>
              {group.instances.map((inst) => (
                <InstanceRow
                  key={inst.id}
                  inst={inst}
                  st={getRowState(inst.id)}
                  rebuildRunning={rebuildRunningIds.has(inst.id)}
                  hasCustomScript={customScriptIds.has(inst.id)}
                  autoRebuildEnabled={autoRebuildIds.has(inst.id)}
                  hasPerm={hasPerm}
                  onAction={onAction}
                  onRebuild={onRebuild}
                  onEdit={onEdit}
                  onDelete={onDelete}
                />
              ))}
            </Fragment>
          ))}
        </tbody>
      </table>
    </div>
  );
}
