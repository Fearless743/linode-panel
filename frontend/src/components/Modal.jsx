import { useEffect, useId, useRef } from "react";

// 通用模态框：点击遮罩 / Esc 关闭，打开时聚焦，关闭后恢复焦点。
// API 保持 { title, onClose, children } 兼容。
export default function Modal({ title, onClose, children }) {
  const dialogRef = useRef(null);
  const titleId = useId();

  // 挂载时聚焦模态框，卸载后恢复此前焦点。
  // 依赖仅挂载一次，避免父组件重渲染（如轮询）导致焦点被反复抢走。
  useEffect(() => {
    const prevFocused = document.activeElement;
    if (dialogRef.current) dialogRef.current.focus();
    return () => {
      if (prevFocused && typeof prevFocused.focus === "function") {
        prevFocused.focus();
      }
    };
  }, []);

  // Esc 关闭（始终绑定最新 onClose，卸载清理）。
  useEffect(() => {
    const onKey = (e) => {
      if (e.key === "Escape") onClose?.();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  return (
    <div
      className="modal-overlay"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose?.();
      }}
    >
      <div
        className="modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        ref={dialogRef}
        tabIndex={-1}
      >
        <h3 className="modal-title" id={titleId}>
          {title}
        </h3>
        <div className="modal-body">{children}</div>
      </div>
    </div>
  );
}
