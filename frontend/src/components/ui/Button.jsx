// 通用按钮：variant / size / loading 驱动，className 透传。
import Spinner from "./Spinner.jsx";

const SIZE_CLASS = { sm: "btn-sm", lg: "btn-lg" };

export default function Button({
  variant = "primary",
  size,
  loading = false,
  disabled = false,
  className,
  children,
  ...rest
}) {
  const { type = "button", ...buttonRest } = rest;

  const classes = ["btn", "btn-" + variant, SIZE_CLASS[size] || "", className]
    .filter(Boolean)
    .join(" ");

  return (
    <button
      type={type}
      className={classes}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      {...buttonRest}
    >
      {loading && <Spinner size="sm" />}
      {children}
    </button>
  );
}
