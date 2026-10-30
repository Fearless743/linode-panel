// 表单字段容器：label + 输入区 + 可选错误文本。
export default function FormField({
  label,
  htmlFor,
  error,
  children,
  className,
}) {
  const classes = ["form-field", className].filter(Boolean).join(" ");
  return (
    <div className={classes}>
      {label && (
        <label className="form-label" htmlFor={htmlFor}>
          {label}
        </label>
      )}
      <div className="form-control">{children}</div>
      {error && (
        <span className="field-error" role="alert">
          {error}
        </span>
      )}
    </div>
  );
}
