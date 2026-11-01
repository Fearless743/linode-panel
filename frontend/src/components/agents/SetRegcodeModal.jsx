import EditFormModal from "../ui/EditFormModal";

// ---------- 设置注册码模态框 ----------
// 注册码用于派生 AES-256-GCM 密钥，加密下发给 Agent 的 PAT 只能被持有该码的 Agent 解密。
// onSetRegcode(code) 由页面编排（API 调用 + banner）；返回 false 视为失败，弹窗内显示错误。
export default function SetRegcodeModal({ onSetRegcode, onClose }) {
  return (
    <EditFormModal
      title="设置注册码"
      description="注册码用于派生 AES-256-GCM 密钥，加密下发给 Agent 的 PAT 只能被持有该码的 Agent 解密。"
      fields={[
        {
          key: "code",
          label: "新注册码",
          type: "text",
          placeholder: "输入新注册码",
          spellCheck: false,
        },
      ]}
      initialValues={{ code: "" }}
      busyLabel="设置中…"
      submitLabel="设置"
      onSubmit={async (values) => {
        const ok = await onSetRegcode(values.code);
        if (ok === false) throw new Error("设置失败");
        onClose();
      }}
      onClose={onClose}
    />
  );
}
