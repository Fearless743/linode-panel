// 加载指示器：size="sm"|"md"，className 透传。
export default function Spinner({ size = "sm", className }) {
  const classes = ["spinner", size === "md" ? "spinner-md" : "", className]
    .filter(Boolean)
    .join(" ");
  return <span className={classes} aria-hidden="true" />;
}
