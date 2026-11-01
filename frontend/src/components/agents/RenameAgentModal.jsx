import EditFormModal from "../ui/EditFormModal";

// ---------- 重命名本地面板 Agent 模态框 ----------
// onRename(agent, name) 由页面编排（API 调用 + 刷新）；抛错时弹窗内显示错误。
export default function RenameAgentModal({ agent, onRename, onClose }) {
  return (
    <EditFormModal
      title="重命名"
      fields={[
        {
          key: "name",
          label: "Agent 名称",
          type: "text",
          placeholder: "如 本机-linode",
          spellCheck: false,
        },
      ]}
      initialValues={{ name: agent ? agent.name : "" }}
      busyLabel="保存中…"
      submitLabel="保存"
      onSubmit={async (values) => {
        const ok = await onRename(agent, values.name);
        if (ok === false) throw new Error("重命名失败");
        onClose();
      }}
      onClose={onClose}
    />
  );
}
