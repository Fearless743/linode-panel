// 通用卡片容器：复用 .panel / .panel-title 样式，可选标题。
export default function Card({ title, className, children, ...rest }) {
  const classes = ["panel", className].filter(Boolean).join(" ");
  return (
    <section className={classes} {...rest}>
      {title && <h3 className="panel-title">{title}</h3>}
      {children}
    </section>
  );
}
