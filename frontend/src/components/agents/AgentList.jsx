// Agent 列表：新增按钮（弹窗）+ 表格（名称 / hostname / 最近心跳 / 待执行 / 重命名 / 删除）。
// 删除使用 ConfirmDialog 确认；本地面板 Agent 可重命名、不可删除。
// onCreate / onDelete / onRename 由页面编排（API 调用 + 刷新）。
import { useState } from "react";
import Button from "../ui/Button.jsx";
import ConfirmDialog from "../ui/ConfirmDialog.jsx";
import CreateAgentModal from "./CreateAgentModal.jsx";
import RenameAgentModal from "./RenameAgentModal.jsx";
import { fmtDateTime } from "../../utils";

export default function AgentList({ agents, onCreate, onDelete, onRename }) {
  const [createOpen, setCreateOpen] = useState(false);
  const [pending, setPending] = useState(null); // 待删除的 Agent
  const [renaming, setRenaming] = useState(null); // 待重命名的 Agent

  return (
    <div className="agents-section">
      <div className="agents-section-head">
        <h3 className="agents-section-title">Agent 列表</h3>
        <Button onClick={() => setCreateOpen(true)}>新增 Agent</Button>
      </div>
      {agents.length === 0 ? (
        <div className="agents-empty">尚无已注册的 Agent</div>
      ) : (
        <table className="agents-table">
          <thead>
            <tr>
              <th>名称</th>
              <th>hostname</th>
              <th>最近心跳</th>
              <th>待执行</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {agents.map((a) => (
              <tr key={a.id}>
                <td>
                  {a.name}
                  {a.local && <span className="tag tag-local">本地</span>}
                </td>
                <td className="agents-muted">{a.hostname || "—"}</td>
                <td className="agents-muted">{fmtDateTime(a.last_seen)}</td>
                <td className="agents-muted">{String(a.pending || 0)}</td>
                <td>
                  {a.local ? (
                    <div className="actions">
                      <Button variant="ghost" onClick={() => setRenaming(a)}>
                        重命名
                      </Button>
                      <span className="agents-muted">内置</span>
                    </div>
                  ) : (
                    <Button variant="ghost" className="btn-danger" onClick={() => setPending(a)}>
                      删除
                    </Button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {createOpen && (
        <CreateAgentModal
          onCreate={onCreate}
          onClose={() => setCreateOpen(false)}
        />
      )}
      {renaming && (
        <RenameAgentModal
          agent={renaming}
          onRename={onRename}
          onClose={() => setRenaming(null)}
        />
      )}
      {pending && (
        <ConfirmDialog
          title="删除 Agent"
          message={'确定删除 Agent「' + pending.name + '」？'}
          confirmText="删除"
          danger
          onConfirm={() => {
            onDelete(pending);
            setPending(null);
          }}
          onCancel={() => setPending(null)}
        />
      )}
    </div>
  );
}
