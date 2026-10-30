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
// token:  面板预签发的 agent token（可空；缺省时退化为 --code 注册流程）；
// code:   注册码（agent 用它派生 PAT 解密密钥，必填）。
// 缺少 origin 时返回空字符串（调用方应禁用复制）。
export function agentInstallCommand({ origin, token, code } = {}) {
  const base = String(origin || "").replace(/\/+$/, "");
  if (!base) return "";
  const parts = ["linode-panel-agent", "--panel", shellQuote(base)];
  if (token) parts.push("--token", shellQuote(token));
  if (code) parts.push("--code", shellQuote(code));
  return parts.join(" ");
}
