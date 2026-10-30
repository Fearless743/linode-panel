// 错误态容器 + 可选重试按钮（Button variant ghost）。
import Button from "./Button.jsx";

export default function ErrorState({
  message,
  onRetry,
  retryText = "重试",
  children,
  className,
}) {
  const classes = ["error-state", className].filter(Boolean).join(" ");
  return (
    <div className={classes} role="alert">
      <p className="error-state-message">{children ?? message}</p>
      {onRetry && (
        <Button variant="ghost" size="sm" onClick={onRetry}>
          {retryText}
        </Button>
      )}
    </div>
  );
}
