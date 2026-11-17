// 与后端 JSON API 通信的轻量客户端。
// 共享契约中的 JSON 字段（instances / status / users / groups / agents…）。
//
// 本模块保持自足：不依赖 constants.js 或其他模块，仅使用浏览器内置 fetch。
// 提供统一封装：
//   - ApiError：带 HTTP status / code / body 的错误对象
//   - apiFetch：超时（AbortController） + 非 2xx 抛 ApiError + 401 回调
//   - query：对象 -> query string
//   - jsonRequest：便捷 JSON POST/PUT/PATCH
//   - 各 REST 端点封装（见文件尾部）

const DEFAULT_TIMEOUT_MS = 15000;

let onUnauthorized = null;

// 注册 401 回调（由 AuthProvider 设置，用于自动登出）。
export function setUnauthorizedHandler(fn) {
  onUnauthorized = fn;
}

// 带 HTTP 状态 / 错误码 / 响应体的 API 错误。
export class ApiError extends Error {
  constructor(message, { status = 0, code = null, body = null } = {}) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.body = body;
  }
}

// 对象 -> query string（跳过 null / undefined / 空串，encodeURIComponent）。
// 返回 "" 或 "?k=v&k2=v2"。
export function query(obj) {
  if (!obj) return "";
  const parts = [];
  for (const [key, value] of Object.entries(obj)) {
    if (value === null || value === undefined || value === "") continue;
    parts.push(encodeURIComponent(key) + "=" + encodeURIComponent(String(value)));
  }
  return parts.length > 0 ? "?" + parts.join("&") : "";
}

// 核心请求：
//   - 超时：默认 15000ms，options.timeout 可覆盖（单位 ms）
//   - 非 2xx：抛 ApiError（status 取自 res.status，code/body 取自 JSON）
//   - 401 且 url 非 /api/auth/login：触发 onUnauthorized
//   - 返回解析后的 JSON body
export async function apiFetch(url, options = {}) {
  const timeout = typeof options.timeout === "number" ? options.timeout : DEFAULT_TIMEOUT_MS;
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeout);
  const { timeout: _timeout, ...rest } = options;
  try {
    const res = await fetch(url, { ...rest, signal: controller.signal });
    let body = null;
    try {
      body = await res.json();
    } catch {
      body = null;
    }
    if (res.status === 401 && url !== "/api/auth/login") {
      if (onUnauthorized) onUnauthorized();
    }
    if (!res.ok) {
      const code =
        body && body.code != null ? body.code : body && typeof body.error === "string" ? body.error : null;
      const msg =
        body && typeof body.error === "string" && body.error
          ? body.error
          : "请求失败（HTTP " + res.status + "）";
      throw new ApiError(msg, { status: res.status, code, body });
    }
    return body;
  } catch (err) {
    if (err && err.name === "AbortError") {
      throw new ApiError("请求超时（" + timeout + "ms）", { status: 0, body: null });
    }
    throw err;
  } finally {
    clearTimeout(timer);
  }
}

// 便捷 JSON POST/PUT/PATCH。
export function jsonRequest(url, method, payload) {
  return apiFetch(url, {
    method,
    headers: { "Content-Type": "application/json" },
    body: payload != null ? JSON.stringify(payload) : undefined,
  });
}

// ---------- 认证 ----------
export function authMe() {
  return apiFetch("/api/auth/me");
}

export function authLogin(username, password) {
  return jsonRequest("/api/auth/login", "POST", { username, password });
}

export function authLogout() {
  return apiFetch("/api/auth/logout", { method: "POST" });
}

// ---------- 实例 ----------
// listInstances({ refresh }) — refresh=true 强制后端请求 Linode API 获取
// 最新实例；默认返回数据库缓存（首次无缓存时仍会拉取）。
export function listInstances({ refresh = false } = {}) {
  return apiFetch("/api/instances" + query({ refresh: refresh ? 1 : undefined }));
}

export function getInstanceStatus(id, tokenId) {
  return apiFetch(
    "/api/instances/" + encodeURIComponent(id) + "/status" + query({ token_id: tokenId })
  );
}

// 通过 API 重新获取单个实例的最新数据（账号分组刷新时逐个调用），并更新其缓存。
export function refreshInstance(id, tokenId) {
  return apiFetch(
    "/api/instances/" + encodeURIComponent(id) + "/refresh" + query({ token_id: tokenId })
  );
}

// 统一迁移：scope=instance 迁移一台不可用实例；scope=account 迁移整个账号。
// payload: { scope, instance_id?, source_token_id?, target_token_id, delete_source?, root_password?, label? }
export function migrate({ scope = "instance", ...payload }) {
  return jsonRequest("/api/migrate", "POST", { scope, ...payload });
}

// 永久删除实例（DELETE /linode/instances/{id}），并清空后端缓存。
export function deleteInstance(id, tokenId) {
  return apiFetch(
    "/api/instances/" + encodeURIComponent(id) + query({ token_id: tokenId }),
    { method: "DELETE" }
  );
}

// action: boot | shutdown | reboot
// viaAgent: true=经 Agent；false=面板直发；undefined/null=跟随账号绑定（后端决定）。
// agentId: 显式指定执行 Agent（仅经 Agent 时生效，覆盖账号绑定）。
export function instanceAction(id, action, { tokenId, viaAgent, agentId } = {}) {
  return apiFetch(
    "/api/instances/" +
      encodeURIComponent(id) +
      "/" +
      encodeURIComponent(action) +
      query({
        token_id: tokenId,
        via_agent: viaAgent === undefined || viaAgent === null ? undefined : viaAgent ? 1 : 0,
        agent_id: viaAgent && agentId ? agentId : undefined,
      }),
    { method: "POST" }
  );
}

// viaAgent: true=经 Agent；false=面板直发；undefined/null=跟随账号绑定（后端决定）。
export function rebuildInstance(id, payload, viaAgent, agentId) {
  return jsonRequest(
    "/api/instances/" +
      encodeURIComponent(id) +
      "/rebuild" +
      query({
        via_agent: viaAgent === undefined || viaAgent === null ? undefined : viaAgent ? 1 : 0,
        agent_id: viaAgent && agentId ? agentId : undefined,
      }),
    "POST",
    payload
  );
}

// ---------- 重建任务 ----------
export function listRebuildTasks() {
  return apiFetch("/api/rebuild/tasks");
}

export function getRebuildTask(id) {
  return apiFetch("/api/rebuild/tasks/" + encodeURIComponent(id));
}

// ---------- 账号级「被墙自愈」统一配置（自动换 IP + 双机轮换） ----------
export function listSelfHeal() {
  return apiFetch("/api/account/self-heal");
}

export function getSelfHeal(tokenId) {
  return apiFetch("/api/account/self-heal/" + encodeURIComponent(tokenId));
}

// payload: { mode:'off'|'rebuild'|'dual', enabled, port, threshold,
//   interval_seconds, script?, root_password?, template_instance_id? }
export function setSelfHeal(tokenId, payload) {
  return jsonRequest("/api/account/self-heal/" + encodeURIComponent(tokenId), "PUT", payload);
}

export function deleteSelfHeal(tokenId) {
  return apiFetch("/api/account/self-heal/" + encodeURIComponent(tokenId), {
    method: "DELETE",
  });
}

export function checkSelfHeal(tokenId) {
  return apiFetch("/api/account/self-heal/" + encodeURIComponent(tokenId) + "/check", {
    method: "POST",
    timeout: 120000,
  });
}

export function rotateSelfHeal(tokenId) {
  return apiFetch("/api/account/self-heal/" + encodeURIComponent(tokenId) + "/rotate", {
    method: "POST",
  });
}

// ---------- 用户 / Token ----------
export function listUsers() {
  return apiFetch("/api/users");
}

export function createUser(data) {
  return jsonRequest("/api/users", "POST", data);
}

export function updateUser(id, patch) {
  return jsonRequest("/api/users/" + encodeURIComponent(id), "PATCH", patch);
}

export function deleteUser(id) {
  return apiFetch("/api/users/" + encodeURIComponent(id), { method: "DELETE" });
}

export function listUserTokens(userId) {
  return apiFetch("/api/users/" + encodeURIComponent(userId) + "/tokens");
}

export function addUserToken(userId, data) {
  return jsonRequest("/api/users/" + encodeURIComponent(userId) + "/tokens", "POST", data);
}

export function deleteUserToken(userId, tokenId) {
  return apiFetch(
    "/api/users/" + encodeURIComponent(userId) + "/tokens/" + encodeURIComponent(tokenId),
    { method: "DELETE" }
  );
}

// ---------- 用户组 ----------
export function listGroups() {
  return apiFetch("/api/groups");
}

export function createGroup(data) {
  return jsonRequest("/api/groups", "POST", data);
}

export function updateGroup(id, patch) {
  return jsonRequest("/api/groups/" + encodeURIComponent(id), "PUT", patch);
}

export function deleteGroup(id) {
  return apiFetch("/api/groups/" + encodeURIComponent(id), { method: "DELETE" });
}

// ---------- Agent ----------
export function listAgents() {
  return apiFetch("/api/agents");
}

export function deleteAgent(id) {
  return apiFetch("/api/agents/" + encodeURIComponent(id), { method: "DELETE" });
}

// 重命名本地面板 agent（持久化，重启后仍生效）。
export function renameAgent(id, name) {
  return jsonRequest("/api/agents/" + encodeURIComponent(id) + "/name", "PUT", { name });
}

export function setRegcode(code) {
  return jsonRequest("/api/agents/regcode", "POST", { code });
}

export function genRegcode() {
  return apiFetch("/api/agents/regcode", { method: "POST" });
}

export function listAgentCommands(limit) {
  return apiFetch("/api/agent/commands" + query({ limit }));
}

// ---------- 账号绑定 Agent ----------
// 当前登录用户可用的 Agent 列表（id + name），用于账号绑定下拉框。
export function listAccountAgents() {
  return apiFetch("/api/account/agents");
}

// 为当前登录用户的账号（token）绑定（agentId>0）或解绑（agentId=0）执行 Agent。
export function setAccountAgent(tokenId, agentId) {
  return jsonRequest(
    "/api/account/tokens/" + encodeURIComponent(tokenId) + "/agent",
    "PUT",
    { agent_id: agentId }
  );
}

// 编辑当前登录用户的账号（token）：修改名称和/或绑定 Agent。
// payload: { label?: string, agent_id?: number }
export function updateAccountToken(tokenId, payload) {
  return jsonRequest("/api/account/tokens/" + encodeURIComponent(tokenId), "PUT", payload);
}

// ---------- TG 通知全局配置（管理员） ----------
// 返回 { enabled, chat_id, has_token }
export function getNotify() {
  return apiFetch("/api/notify");
}

// payload: { enabled, chat_id, bot_token? }（bot_token 留空表示不改）
export function setNotify(payload) {
  return jsonRequest("/api/notify", "PUT", payload);
}

export function testNotify() {
  return apiFetch("/api/notify/test", { method: "POST" });
}
