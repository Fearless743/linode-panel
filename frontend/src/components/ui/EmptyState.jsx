// 空态容器：复用 .empty-state 风格类。
export default function EmptyState({ text, children, className }) {
  const classes = ["empty-state", className].filter(Boolean).join(" ");
  return (
    <div className={classes} role="status">
      {children ?? text}
    </div>
  );
}
