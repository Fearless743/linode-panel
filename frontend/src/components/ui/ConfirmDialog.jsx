// 确认对话框：复用 Modal 行为（遮罩点击 / Esc 关闭），危险操作用 danger 样式。
import { useEffect } from "react";
import Modal from "../Modal.jsx";
import Button from "./Button.jsx";

export default function ConfirmDialog({
  title,
  message,
  confirmText = "确认",
  cancelText = "取消",
  danger = false,
  onConfirm,
  onCancel,
}) {
  useEffect(() => {
    const onKey = (e) => {
      if (e.key === "Escape") onCancel?.();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onCancel]);

  return (
    <Modal title={title} onClose={onCancel}>
      <p className="modal-detail">{message}</p>
      <div className="modal-actions">
        <Button variant="ghost" onClick={onCancel}>
          {cancelText}
        </Button>
        <Button
          variant={danger ? "danger" : "primary"}
          onClick={onConfirm}
        >
          {confirmText}
        </Button>
      </div>
    </Modal>
  );
}
