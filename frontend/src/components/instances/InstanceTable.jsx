// 实例表格：表头 + 实例行列表，行内包含操作按钮与 SSH 卡片。
import InstanceRow from "./InstanceRow";

export default function InstanceTable({
  instances,
  getRowState,
  rebuildTaskRunning,
  customScriptIds,
  autoRebuildIds,
  hasPerm,
  onAction,
  onRebuild,
  onScript,
  onAutoRebuild,
  onRefresh,
}) {
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
          {instances.map((inst) => (
            <InstanceRow
              key={inst.id}
              inst={inst}
              st={getRowState(inst.id)}
              rebuildTaskRunning={rebuildTaskRunning}
              hasCustomScript={customScriptIds.has(inst.id)}
              autoRebuildEnabled={autoRebuildIds.has(inst.id)}
              hasPerm={hasPerm}
              onAction={onAction}
              onRebuild={onRebuild}
              onScript={onScript}
              onAutoRebuild={onAutoRebuild}
              onRefresh={onRefresh}
            />
          ))}
        </tbody>
      </table>
    </div>
  );
}
