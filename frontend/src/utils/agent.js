// Agent 一键安装命令生成。

// shellQuote 在值包含 shell 特殊字符时用单引号包裹，避免面板地址 /
// token 中出现意外字符时破坏命令。
function shellQuote(value) {
  const s = String(value);
  if (/^[A-Za-z0-9_@%+=:,./-]+$/.test(s)) return s;
  return "'" + s.replace(/'/g, "'\\''") + "'";
}

// agentInstallCommand 拼出远程主机上接入面板 agent 的一条命令。
// origin: 面板 base URL（通常取 window.location.origin）；
// code:   注册码（agent 用它注册并获取身份，必填）。
// 缺少 origin 时返回空字符串（调用方应禁用复制）。
export function agentInstallCommand({ origin, code } = {}) {
  const base = String(origin || "").replace(/\/+$/, "");
  if (!base) return "";
  const parts = ["linode-panel-agent", "--panel", shellQuote(base)];
  if (code) parts.push("--code", shellQuote(code));
  return parts.join(" ");
}
