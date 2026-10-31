// 开关组件：用于单值布尔切换（如权限项 enabled/disabled）。
// 渲染为带轨道 + 滑块的 button，带视觉反馈；可用 role="switch" + aria-checked 无障碍。
export default function Switch({ checked, onChange, label, disabled = false, ...rest }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={!!checked}
      aria-label={label}
      className={
        "switch" + (checked ? " switch-on" : "") + (disabled ? " switch-disabled" : "")
      }
      onClick={() => {
        if (!disabled) onChange(!checked);
      }}
      disabled={disabled}
      {...rest}
    >
      <span className="switch-knob" aria-hidden="true" />
    </button>
  );
}
