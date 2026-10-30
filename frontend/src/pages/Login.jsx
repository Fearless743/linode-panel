import { useState } from "react";
import { Navigate, useNavigate } from "react-router-dom";
import { useAuth } from "../auth";
import { Button, ErrorState, FormField } from "../components/ui";

export default function Login() {
  const { user, login } = useAuth();
  const nav = useNavigate();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [fieldErrors, setFieldErrors] = useState({});
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  // 已登录 → 直接进入面板。
  if (user) return <Navigate to="/" replace />;

  // 表单校验：用户名/密码非空。
  function validate() {
    const errs = {};
    if (!username.trim()) errs.username = "请输入用户名";
    if (!password) errs.password = "请输入密码";
    setFieldErrors(errs);
    return Object.keys(errs).length === 0;
  }

  async function onSubmit(e) {
    e.preventDefault();
    setError("");
    if (!validate()) return;
    setBusy(true);
    try {
      await login(username.trim(), password);
      nav("/", { replace: true });
    } catch (err) {
      // ApiError.message 提供可读错误描述（如“用户名或密码错误”）。
      setError(err && err.message ? err.message : "登录失败");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="login-view">
      <form className="login-card" onSubmit={onSubmit} noValidate>
        <h1 className="login-title">Linode 开机面板</h1>
        <FormField
          label="用户名"
          htmlFor="login-username"
          error={fieldErrors.username}
        >
          <input
            id="login-username"
            className="login-input"
            type="text"
            autoComplete="username"
            spellCheck={false}
            value={username}
            onChange={(e) => setUsername(e.target.value)}
          />
        </FormField>
        <FormField
          label="密码"
          htmlFor="login-password"
          error={fieldErrors.password}
        >
          <input
            id="login-password"
            className="login-input"
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </FormField>
        <Button
          type="submit"
          className="login-submit"
          loading={busy}
          disabled={busy}
        >
          {busy ? "登录中…" : "登录"}
        </Button>
        {error && <ErrorState message={error} className="login-error" />}
      </form>
    </div>
  );
}
