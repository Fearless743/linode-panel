import { useState, useId } from "react";
import Modal from "../Modal";
import Switch from "./Switch.jsx";

// 统一编辑弹窗：各页面的「编辑」操作（改名称、绑定 Agent、改组、重置密码等）
// 都收敛到这里。调用方传入字段描述数组，组件负责渲染表单 + 取消/提交按钮。
//
// 字段 type:
//   "text" / "password"  — 单值输入，Enter 触发提交
//   "select"            — 下拉选择，options: [{value, label}]
//   "switch"            — 单个布尔开关
//   "switches"          — 多开关网格，值为数组（如权限列表），options: [{value, label}]
//
// 用法：
//   <EditFormModal
//     title="编辑账号"
//     description="修改账号名称与绑定 Agent"
//     fields={[
//       { key: "label", label: "名称", type: "text", placeholder: "名称（如 生产账号）" },
//       { key: "agent_id", label: "执行 Agent", type: "select",
//         options: [{ value: "", label: "未绑定（默认 Agent）" }, ...] },
//       { key: "perms", label: "权限", type: "switches",
//         options: [{ value: "view", label: "查看" }, ...] },
//     ]}
//     initialValues={{ label: "生产账号", agent_id: "", perms: [] }}
//     submitLabel="保存"
//     onSubmit={async (values) => { await updateXxx(id, values); }}
//     onClose={...}
//   />
//
// onSubmit 抛错时错误留在弹窗内（modal-error 区域）；成功后调用方可直接 onClose()。
export default function EditFormModal({
  title,
  description,
  fields,
  initialValues = {},
  submitLabel = "保存",
  busyLabel,
  onSubmit,
  onClose,
}) {
  const idPrefix = useId();
  const [values, setValues] = useState(initialValues);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  function set(key, value) {
    setValues((v) => ({ ...v, [key]: value }));
  }

  async function submit() {
    // 必填校验：默认必填；select 的占位项（value=""）调用方可传 required:false 放开；
    // switch / switches 不做必填校验（开关语义是可选勾选）。
    for (const f of fields) {
      if (f.required === false) continue;
      if (f.type === "switch" || f.type === "switches") continue;
      const raw = values[f.key];
      if (f.type === "select") {
        if (raw === "" || raw == null) {
          setError((f.requiredLabel || f.label) + " 不能为空");
          return;
        }
      } else if (raw == null || String(raw).trim() === "") {
        setError((f.requiredLabel || f.label) + " 不能为空");
        return;
      }
    }
    setError("");
    setBusy(true);
    try {
      await onSubmit({ ...values });
      // 成功：由调用方决定 onClose；这里不主动关，允许调用方保留弹窗场景。
    } catch (err) {
      setError((err && err.message ? err.message : "保存失败"));
      setBusy(false);
    }
  }

  return (
    <Modal title={title} onClose={onClose}>
      {description && <p className="modal-detail">{description}</p>}
      {fields.map((f) => {
        const inputId = idPrefix + "-" + f.key;
        const common = {
          id: inputId,
          className: "modal-input",
          value: values[f.key] == null ? "" : values[f.key],
          onChange: (e) => set(f.key, e.target.value),
          disabled: busy,
          "aria-label": f.label,
        };
        return (
          <div className="modal-field" key={f.key}>
            {f.type === "switches" ? (
              <div>
                <label>{f.label}</label>
                <div className="perm-checks">
                  {(f.options || []).map((o) => (
                    <label className="perm-check" key={o.value}>
                      <Switch
                        checked={
                          Array.isArray(values[f.key]) &&
                          values[f.key].includes(o.value)
                        }
                        onChange={() =>
                          set(
                            f.key,
                            Array.isArray(values[f.key]) &&
                            values[f.key].includes(o.value)
                              ? values[f.key].filter((x) => x !== o.value)
                              : [...(values[f.key] || []), o.value]
                          )
                        }
                        disabled={busy}
                        label={o.label}
                      />
                      {o.label}
                    </label>
                  ))}
                </div>
              </div>
            ) : f.type === "switch" ? (
              <div className="switch-row">
                <label htmlFor={inputId}>{f.label}</label>
                <Switch
                  checked={!!values[f.key]}
                  onChange={(next) => set(f.key, next)}
                  disabled={busy}
                  label={f.label}
                />
              </div>
            ) : (
              <>
                <label htmlFor={inputId}>{f.label}</label>
                {f.type === "select" ? (
                  <select {...common}>
                    {(f.options || []).map((o) => (
                      <option key={o.value} value={o.value}>
                        {o.label}
                      </option>
                    ))}
                  </select>
                ) : (
                  <input
                    {...common}
                    type={f.type || "text"}
                    placeholder={f.placeholder}
                    spellCheck={f.spellCheck}
                    autoComplete={f.autoComplete}
                    onKeyDown={(e) => {
                      if (e.key === "Enter" && f.type !== "textarea") submit();
                    }}
                  />
                )}
              </>
            )}
          </div>
        );
      })}
      <div className="modal-error">{error}</div>
      <div className="modal-actions">
        <button className="btn btn-ghost" type="button" onClick={onClose} disabled={busy}>
          取消
        </button>
        <button className="btn btn-primary" type="button" onClick={submit} disabled={busy}>
          {busy ? (busyLabel || "保存中…") : submitLabel}
        </button>
      </div>
    </Modal>
  );
}
