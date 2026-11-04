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

// 手动迁移：上游已不存在的实例用缓存配置在另一个账号上重建，并把面板配置
// （脚本、root 密码、自动换 IP、替换配置）迁移到新账号/新实例。
// payload: { token_id（来源账号）, target_token_id, root_password?, label? }
export function migrateInstance(id, payload) {
  return jsonRequest(
    "/api/instances/" + encodeURIComponent(id) + "/migrate",
    "POST",
    payload
  );
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

// ---------- 被墙后自动重建换 IP ----------
export function getAutoRebuild(id) {
  return apiFetch("/api/instances/" + encodeURIComponent(id) + "/auto-rebuild");
}

// payload: { enabled, port, threshold, interval_seconds, token_id }
export function setAutoRebuild(id, payload) {
  return jsonRequest("/api/instances/" + encodeURIComponent(id) + "/auto-rebuild", "PUT", payload);
}

export function deleteAutoRebuild(id) {
  return apiFetch("/api/instances/" + encodeURIComponent(id) + "/auto-rebuild", {
    method: "DELETE",
  });
}

// 立即探测一次并返回最新状态。
export function checkAutoRebuild(id) {
  return apiFetch("/api/instances/" + encodeURIComponent(id) + "/auto-rebuild/check", {
    method: "POST",
  });
}

// 已配置自动换 IP 的实例列表（用于行内标记）。
export function listAutoRebuild() {
  return apiFetch("/api/auto-rebuild");
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

// ---------- 账号组 ----------
// 账号组：把多个账号（token）绑定成组；组内某账号实例被封号/无余额时，
// 可用相同配置在组内另一账号重建替换。
// 组对象：{ id, user_id, name, token_ids: [], created_at }
export function listAccountGroups() {
  return apiFetch("/api/account/groups");
}

// 新建账号组，返回创建好的组对象。
export function createAccountGroup(name) {
  return jsonRequest("/api/account/groups", "POST", { name });
}

// 重命名账号组。
export function renameAccountGroup(groupId, name) {
  return jsonRequest("/api/account/groups/" + encodeURIComponent(groupId), "PUT", { name });
}

export function deleteAccountGroup(groupId) {
  return apiFetch("/api/account/groups/" + encodeURIComponent(groupId), { method: "DELETE" });
}

// 把账号（token）加入账号组。
export function addAccountGroupToken(groupId, tokenId) {
  return jsonRequest(
    "/api/account/groups/" + encodeURIComponent(groupId) + "/tokens",
    "POST",
    { token_id: tokenId }
  );
}

// 把账号（token）移出账号组。
export function removeAccountGroupToken(groupId, tokenId) {
  return apiFetch(
    "/api/account/groups/" +
      encodeURIComponent(groupId) +
      "/tokens/" +
      encodeURIComponent(tokenId),
    { method: "DELETE" }
  );
}

// 获取账号组的目标开机配置。
// 返回 { config: { region, type, image, label, script, booted } | null }
export function getInstanceConfig(groupId) {
  return apiFetch(
    "/api/account/groups/" + encodeURIComponent(groupId) + "/instance-config"
  );
}

// 设置或清除账号组的目标开机配置。
// config 为 null 表示清除配置；否则须含 region/type/image/label。
export function setInstanceConfig(groupId, config) {
  return jsonRequest(
    "/api/account/groups/" + encodeURIComponent(groupId) + "/instance-config",
    "PUT",
    { config }
  );
}

// ---------- 实例自动替换（账号组内跨账号） ----------
// 按实例配置：某实例所在账号被封禁/余额不足时，在所属账号组内换另一个账号，
// 用相同配置重建替换。
// 返回 { failovers: [{ instance_id, user_id, group_id, token_id, enabled,
//   threshold, interval_seconds, fail_count, last_status, last_detail,
//   last_check_at, last_task_id, replaced_by_instance_id, updated_at }] }
export function listFailovers() {
  return apiFetch("/api/account/failover");
}

// 保存实例的替换配置；enabled=false 表示关闭但仍保留配置。
// payload: { token_id, group_id, enabled, threshold, interval_seconds }
export function setInstanceFailover(instanceId, payload) {
  return jsonRequest(
    "/api/account/failover/" + encodeURIComponent(instanceId),
    "PUT",
    payload
  );
}

// 删除实例的替换配置。
export function deleteInstanceFailover(instanceId) {
  return apiFetch("/api/account/failover/" + encodeURIComponent(instanceId), {
    method: "DELETE",
  });
}

// ---------- 双机轮询替换（账号维度，一对 A/B） ----------
// 返回 { dual: [{ token_id, user_id, enabled, port, threshold, interval_seconds,
//   active_instance_id, standby_instance_id, state, fail_count, last_status,
//   last_detail, last_check_at, script, has_root_password, updated_at }] }
export function listDual() {
  return apiFetch("/api/account/dual");
}

// 保存账号的双机配置；启用时需要 template_instance_id（模板机作为 A）。
// payload: { enabled, template_instance_id?, port, threshold, interval_seconds, script?, root_password? }
// 返回 { dual }，失败时可能附带 warning（B 创建失败但配置已保存）。
export function setDual(tokenId, payload) {
  return jsonRequest("/api/account/dual/" + encodeURIComponent(tokenId), "PUT", payload);
}

export function deleteDual(tokenId) {
  return apiFetch("/api/account/dual/" + encodeURIComponent(tokenId), {
    method: "DELETE",
  });
}

// 立即探测一次并返回最新状态。
export function checkDual(tokenId) {
  return apiFetch("/api/account/dual/" + encodeURIComponent(tokenId) + "/check", {
    method: "POST",
  });
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
