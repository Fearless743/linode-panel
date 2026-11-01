import { useState } from "react";
import { useUi } from "../../ui";
import { createGroup, deleteGroup } from "../../api";
import ConfirmDialog from "../ui/ConfirmDialog";
import EditFormModal from "../ui/EditFormModal";
import { PERM_LABELS } from "../../constants";

// ---------- 创建权限组模态框 ----------
function CreateGroupModal({ onClose, onCreated }) {
  return (
    <EditFormModal
      title="创建权限组"
      fields={[
        {
          key: "name",
          label: "组名称",
          type: "text",
          autoComplete: "off",
        },
        {
          key: "perms",
          label: "权限",
          type: "switches",
          options: PERM_LABELS.map(([value, label]) => ({ value, label })),
        },
      ]}
      initialValues={{ name: "", perms: ["view"] }}
      busyLabel="创建中…"
      submitLabel="创建"
      onSubmit={async (values) => {
        await createGroup({ name: values.name.trim(), perms: values.perms || [] });
        onCreated();
        onClose();
      }}
      onClose={onClose}
    />
  );
}

// ---------- 权限组 tab ----------
export default function GroupsTab({ groups, users, onEditGroup, onReload }) {
  const { showBanner } = useUi();
  const [createOpen, setCreateOpen] = useState(false);
  const [delGroup, setDelGroup] = useState(null);

  async function doDelete() {
    if (!delGroup) return;
    try {
      await deleteGroup(delGroup.id);
      setDelGroup(null);
      await onReload();
    } catch (err) {
      setDelGroup(null);
      showBanner(err && err.message ? err.message : "删除权限组失败", false);
    }
  }

  return (
    <div className="users-tab-content">
      <div className="user-create">
        <div className="user-create-title">新建权限组</div>
        <div className="user-create-row">
          <button className="btn btn-primary" type="button" onClick={() => setCreateOpen(true)}>
            创建权限组
          </button>
        </div>
      </div>
      {groups !== null && groups.length === 0 && <div className="users-empty">暂无权限组</div>}
      {groups !== null && groups.length > 0 && (
        <table className="users-table">
          <thead>
            <tr>
              <th>名称</th>
              <th>权限</th>
              <th>用户数</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {groups.map((g) => (
              <tr key={g.id}>
                <td>{g.name || ""}</td>
                <td>
                  <div className="perm-chips">
                    {(Array.isArray(g.perms) ? g.perms : []).length === 0 ? (
                      <span className="perm-chip">无</span>
                    ) : (
                      PERM_LABELS.filter(([p]) => g.perms.includes(p)).map(([p, text]) => (
                        <span className="perm-chip" key={p}>
                          {text}
                        </span>
                      ))
                    )}
                  </div>
                </td>
                <td>{(users || []).filter((u) => u.group_id === g.id).length}</td>
                <td>
                  <div className="actions">
                    <button className="action-btn" type="button" onClick={() => onEditGroup(g)}>
                      编辑
                    </button>
                    <button className="action-btn danger" type="button" onClick={() => setDelGroup(g)}>
                      删除
                    </button>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {createOpen && (
        <CreateGroupModal onClose={() => setCreateOpen(false)} onCreated={onReload} />
      )}
      {delGroup && (
        <ConfirmDialog
          title="删除权限组"
          message={"确定删除权限组 " + delGroup.name + " ？"}
          confirmText="删除"
          danger
          onConfirm={doDelete}
          onCancel={() => setDelGroup(null)}
        />
      )}
    </div>
  );
}
