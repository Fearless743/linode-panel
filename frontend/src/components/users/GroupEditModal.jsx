import { PERM_LABELS } from "../../constants";
import { updateGroup } from "../../api";
import EditFormModal from "../ui/EditFormModal";

// ---------- 权限组编辑模态框 ----------
export default function GroupEditModal({ group, onClose, onSaved }) {
  return (
    <EditFormModal
      title="编辑权限组"
      fields={[
        { key: "name", label: "组名称", type: "text" },
        {
          key: "perms",
          label: "权限",
          type: "switches",
          options: PERM_LABELS.map(([value, label]) => ({ value, label })),
        },
      ]}
      initialValues={{
        name: group.name || "",
        perms: Array.isArray(group.perms) ? group.perms : [],
      }}
      busyLabel="保存中…"
      onSubmit={async (values) => {
        await updateGroup(group.id, { name: values.name.trim(), perms: values.perms || [] });
        onSaved();
        onClose();
      }}
      onClose={onClose}
    />
  );
}
