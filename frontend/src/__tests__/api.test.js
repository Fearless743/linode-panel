// API 客户端单元测试：ApiError / query / apiFetch / 端点 URL 与方法。
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  ApiError,
  apiFetch,
  query,
  setUnauthorizedHandler,
  authLogin,
  authMe,
  authLogout,
  listInstances,
  getInstanceStatus,
  instanceAction,
  rebuildInstance,
  createUser,
  listGroups,
  listAgents,
  setRegcode,
  genRegcode,
  listAgentCommands,
  listAccountAgents,
  setAccountAgent,
  listAccountGroups,
  createAccountGroup,
  renameAccountGroup,
  deleteAccountGroup,
  addAccountGroupToken,
  removeAccountGroupToken,
  listFailovers,
  setInstanceFailover,
  deleteInstanceFailover,
} from "../api";

function mockResponse(body, { ok = true, status = 200 } = {}) {
  return { ok, status, json: vi.fn().mockResolvedValue(body) };
}

describe("query", () => {
  it("空值返回空字符串", () => {
    expect(query(null)).toBe("");
    expect(query(undefined)).toBe("");
    expect(query({})).toBe("");
  });

  it("跳过 null / undefined / 空串，保留 0", () => {
    expect(query({ a: null, b: undefined, c: "", d: 0 })).toBe("?d=0");
  });

  it("对 key 和 value 做编码", () => {
    expect(query({ "a b": "x y", c: "a&b" })).toBe("?a%20b=x%20y&c=a%26b");
  });

  it("多个键按对象顺序拼接", () => {
    expect(query({ x: 1, y: "z" })).toBe("?x=1&y=z");
  });
});

describe("ApiError", () => {
  it("构造并携带 status/code/body", () => {
    const body = { error: "boom" };
    const err = new ApiError("boom", { status: 500, code: "E500", body });
    expect(err).toBeInstanceOf(Error);
    expect(err.name).toBe("ApiError");
    expect(err.message).toBe("boom");
    expect(err.status).toBe(500);
    expect(err.code).toBe("E500");
    expect(err.body).toBe(body);
  });

  it("默认值 status=0 code=null body=null", () => {
    const err = new ApiError("plain");
    expect(err.status).toBe(0);
    expect(err.code).toBeNull();
    expect(err.body).toBeNull();
  });
});

describe("apiFetch", () => {
  beforeEach(() => {
    vi.stubGlobal("fetch", vi.fn());
  });

  afterEach(() => {
    setUnauthorizedHandler(null);
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it("2xx 返回解析后的 JSON", async () => {
    const data = { instances: [] };
    vi.mocked(fetch).mockResolvedValue(mockResponse(data));
    await expect(apiFetch("/api/instances")).resolves.toEqual(data);
    expect(fetch).toHaveBeenCalledWith(
      "/api/instances",
      expect.objectContaining({ signal: expect.anything() })
    );
  });

  it("非 2xx 抛 ApiError（message 取自 body.error）", async () => {
    vi.mocked(fetch).mockResolvedValue(
      mockResponse({ error: "bad request" }, { ok: false, status: 400 })
    );
    await expect(apiFetch("/api/x")).rejects.toMatchObject({
      name: "ApiError",
      status: 400,
      code: "bad request",
      message: "bad request",
    });
  });

  it("非 2xx 且无 error 字段时用兜底 message 与 body.code", async () => {
    vi.mocked(fetch).mockResolvedValue(
      mockResponse({ code: "E123" }, { ok: false, status: 502 })
    );
    await expect(apiFetch("/api/x")).rejects.toMatchObject({
      status: 502,
      code: "E123",
      message: "请求失败（HTTP 502）",
    });
  });

  it("JSON 解析失败仍抛 ApiError", async () => {
    vi.mocked(fetch).mockResolvedValue({
      ok: false,
      status: 500,
      json: vi.fn().mockRejectedValue(new SyntaxError("bad json")),
    });
    await expect(apiFetch("/api/x")).rejects.toMatchObject({
      name: "ApiError",
      status: 500,
      message: "请求失败（HTTP 500）",
    });
  });

  it("401 且非 login 时触发 onUnauthorized", async () => {
    const onUnauthorized = vi.fn();
    setUnauthorizedHandler(onUnauthorized);
    vi.mocked(fetch).mockResolvedValue(
      mockResponse({ error: "unauthorized" }, { ok: false, status: 401 })
    );
    await expect(apiFetch("/api/instances")).rejects.toMatchObject({ status: 401 });
    expect(onUnauthorized).toHaveBeenCalledTimes(1);
  });

  it("login 的 401 不触发 onUnauthorized", async () => {
    const onUnauthorized = vi.fn();
    setUnauthorizedHandler(onUnauthorized);
    vi.mocked(fetch).mockResolvedValue(
      mockResponse({ error: "bad credentials" }, { ok: false, status: 401 })
    );
    await expect(apiFetch("/api/auth/login", { method: "POST" })).rejects.toMatchObject({
      status: 401,
    });
    expect(onUnauthorized).not.toHaveBeenCalled();
  });

  it("超时抛出 ApiError（status=0）", async () => {
    vi.useFakeTimers();
    vi.mocked(fetch).mockImplementation(
      (_url, options) =>
        new Promise((_resolve, reject) => {
          options.signal.addEventListener("abort", () => {
            reject(Object.assign(new Error("aborted"), { name: "AbortError" }));
          });
        })
    );
    const promise = apiFetch("/api/slow", { timeout: 1000 });
    const assertion = expect(promise).rejects.toMatchObject({
      name: "ApiError",
      status: 0,
      message: "请求超时（1000ms）",
    });
    await vi.advanceTimersByTimeAsync(1000);
    await assertion;
  });
});

describe("端点封装", () => {
  beforeEach(() => {
    vi.stubGlobal("fetch", vi.fn());
  });

  afterEach(() => {
    setUnauthorizedHandler(null);
    vi.unstubAllGlobals();
  });

  async function lastCall() {
    const calls = vi.mocked(fetch).mock.calls;
    return calls[calls.length - 1];
  }

  it("authMe GET /api/auth/me", async () => {
    vi.mocked(fetch).mockResolvedValue(mockResponse({ user: "u" }));
    await authMe();
    const [url, opts] = await lastCall();
    expect(url).toBe("/api/auth/me");
    expect(opts.method).toBeUndefined();
  });

  it("authLogin POST /api/auth/login 带 JSON body", async () => {
    vi.mocked(fetch).mockResolvedValue(mockResponse({ token: "t" }));
    await authLogin("admin", "pw");
    const [url, opts] = await lastCall();
    expect(url).toBe("/api/auth/login");
    expect(opts.method).toBe("POST");
    expect(JSON.parse(opts.body)).toEqual({ username: "admin", password: "pw" });
  });

  it("authLogout POST /api/auth/logout", async () => {
    vi.mocked(fetch).mockResolvedValue(mockResponse(null));
    await authLogout();
    const [url, opts] = await lastCall();
    expect(url).toBe("/api/auth/logout");
    expect(opts.method).toBe("POST");
  });

  it("listInstances GET /api/instances", async () => {
    vi.mocked(fetch).mockResolvedValue(mockResponse([]));
    await listInstances();
    const [url] = await lastCall();
    expect(url).toBe("/api/instances");
  });

  it("listInstances refresh 附带 ?refresh=1", async () => {
    vi.mocked(fetch).mockResolvedValue(mockResponse([]));
    await listInstances({ refresh: true });
    const [url] = await lastCall();
    expect(url).toBe("/api/instances?refresh=1");
  });

  it("getInstanceStatus 编码 id 并附带 token_id", async () => {
    vi.mocked(fetch).mockResolvedValue(mockResponse({ status: "running" }));
    await getInstanceStatus("id with space", 42);
    const [url] = await lastCall();
    expect(url).toBe("/api/instances/id%20with%20space/status?token_id=42");
  });

  it("instanceAction 编码 action 与 via_agent", async () => {
    vi.mocked(fetch).mockResolvedValue(mockResponse(null));
    await instanceAction("abc/1", "reboot", { tokenId: 7, viaAgent: true });
    const [url, opts] = await lastCall();
    expect(url).toBe("/api/instances/abc%2F1/reboot?token_id=7&via_agent=1");
    expect(opts.method).toBe("POST");
  });

  it("instanceAction 指定 agent_id 覆盖账号绑定", async () => {
    vi.mocked(fetch).mockResolvedValue(mockResponse(null));
    await instanceAction("7", "boot", { tokenId: 7, viaAgent: true, agentId: 3 });
    const [url] = await lastCall();
    expect(url).toBe("/api/instances/7/boot?token_id=7&via_agent=1&agent_id=3");
  });

  it("rebuildInstance POST JSON + via_agent", async () => {
    vi.mocked(fetch).mockResolvedValue(mockResponse({ task: "t1" }));
    await rebuildInstance("i1", { script: "s" }, true);
    const [url, opts] = await lastCall();
    expect(url).toBe("/api/instances/i1/rebuild?via_agent=1");
    expect(opts.method).toBe("POST");
    expect(JSON.parse(opts.body)).toEqual({ script: "s" });
  });

  it("rebuildInstance 指定 agent_id", async () => {
    vi.mocked(fetch).mockResolvedValue(mockResponse({ task: "t1" }));
    await rebuildInstance("i1", { script: "s" }, true, 5);
    const [url] = await lastCall();
    expect(url).toBe("/api/instances/i1/rebuild?via_agent=1&agent_id=5");
  });

  it("createUser POST /api/users", async () => {
    vi.mocked(fetch).mockResolvedValue(mockResponse({ id: 1 }));
    await createUser({ username: "a", password: "p" });
    const [url, opts] = await lastCall();
    expect(url).toBe("/api/users");
    expect(opts.method).toBe("POST");
    expect(JSON.parse(opts.body)).toEqual({ username: "a", password: "p" });
  });

  it("listGroups GET /api/groups", async () => {
    vi.mocked(fetch).mockResolvedValue(mockResponse([]));
    await listGroups();
    const [url] = await lastCall();
    expect(url).toBe("/api/groups");
  });

  it("listAgents GET /api/agents", async () => {
    vi.mocked(fetch).mockResolvedValue(mockResponse([]));
    await listAgents();
    const [url] = await lastCall();
    expect(url).toBe("/api/agents");
  });

  it("setRegcode POST /api/agents/regcode", async () => {
    vi.mocked(fetch).mockResolvedValue(mockResponse(null));
    await setRegcode("CODE");
    const [url, opts] = await lastCall();
    expect(url).toBe("/api/agents/regcode");
    expect(opts.method).toBe("POST");
    expect(JSON.parse(opts.body)).toEqual({ code: "CODE" });
  });

  it("genRegcode POST /api/agents/regcode（无 body）", async () => {
    vi.mocked(fetch).mockResolvedValue(mockResponse({ code: "X" }));
    await genRegcode();
    const [url, opts] = await lastCall();
    expect(url).toBe("/api/agents/regcode");
    expect(opts.method).toBe("POST");
  });

  it("listAgentCommands 附带 limit 查询", async () => {
    vi.mocked(fetch).mockResolvedValue(mockResponse([]));
    await listAgentCommands(50);
    const [url] = await lastCall();
    expect(url).toBe("/api/agent/commands?limit=50");
  });

  it("listAccountAgents GET /api/account/agents", async () => {
    vi.mocked(fetch).mockResolvedValue(mockResponse([]));
    await listAccountAgents();
    const [url] = await lastCall();
    expect(url).toBe("/api/account/agents");
  });

  it("setAccountAgent PUT 绑定 agent_id", async () => {
    vi.mocked(fetch).mockResolvedValue(mockResponse({ ok: true }));
    await setAccountAgent(7, 3);
    const [url, opts] = await lastCall();
    expect(url).toBe("/api/account/tokens/7/agent");
    expect(opts.method).toBe("PUT");
    expect(JSON.parse(opts.body)).toEqual({ agent_id: 3 });
  });

  it("setAccountAgent agent_id=0 解绑", async () => {
    vi.mocked(fetch).mockResolvedValue(mockResponse({ ok: true }));
    await setAccountAgent(7, 0);
    const [url, opts] = await lastCall();
    expect(url).toBe("/api/account/tokens/7/agent");
    expect(JSON.parse(opts.body)).toEqual({ agent_id: 0 });
  });

  it("listAccountGroups GET /api/account/groups 并透传返回值", async () => {
    const data = {
      groups: [{ id: 1, user_id: 2, name: "g1", token_ids: [3], created_at: "2026-01-01" }],
    };
    vi.mocked(fetch).mockResolvedValue(mockResponse(data));
    await expect(listAccountGroups()).resolves.toEqual(data);
    const [url, opts] = await lastCall();
    expect(url).toBe("/api/account/groups");
    expect(opts.method).toBeUndefined();
  });

  it("createAccountGroup POST /api/account/groups 带 name", async () => {
    const group = { id: 1, user_id: 2, name: "g1", token_ids: [], created_at: "2026-01-01" };
    vi.mocked(fetch).mockResolvedValue(mockResponse(group, { status: 201 }));
    await expect(createAccountGroup("g1")).resolves.toEqual(group);
    const [url, opts] = await lastCall();
    expect(url).toBe("/api/account/groups");
    expect(opts.method).toBe("POST");
    expect(JSON.parse(opts.body)).toEqual({ name: "g1" });
  });

  it("renameAccountGroup PUT 编码 groupId 并带 name", async () => {
    vi.mocked(fetch).mockResolvedValue(mockResponse({ ok: true }));
    await expect(renameAccountGroup("g/1", "new")).resolves.toEqual({ ok: true });
    const [url, opts] = await lastCall();
    expect(url).toBe("/api/account/groups/g%2F1");
    expect(opts.method).toBe("PUT");
    expect(JSON.parse(opts.body)).toEqual({ name: "new" });
  });

  it("deleteAccountGroup DELETE 编码 groupId", async () => {
    vi.mocked(fetch).mockResolvedValue(mockResponse({ ok: true }));
    await expect(deleteAccountGroup("g 1")).resolves.toEqual({ ok: true });
    const [url, opts] = await lastCall();
    expect(url).toBe("/api/account/groups/g%201");
    expect(opts.method).toBe("DELETE");
    expect(opts.body).toBeUndefined();
  });

  it("addAccountGroupToken POST 编码 groupId 并带 token_id", async () => {
    vi.mocked(fetch).mockResolvedValue(mockResponse({ ok: true }));
    await expect(addAccountGroupToken(1, "t/2")).resolves.toEqual({ ok: true });
    const [url, opts] = await lastCall();
    expect(url).toBe("/api/account/groups/1/tokens");
    expect(opts.method).toBe("POST");
    expect(JSON.parse(opts.body)).toEqual({ token_id: "t/2" });
  });

  it("removeAccountGroupToken DELETE 编码 groupId 与 tokenId", async () => {
    vi.mocked(fetch).mockResolvedValue(mockResponse({ ok: true }));
    await expect(removeAccountGroupToken("g 1", "t/2")).resolves.toEqual({ ok: true });
    const [url, opts] = await lastCall();
    expect(url).toBe("/api/account/groups/g%201/tokens/t%2F2");
    expect(opts.method).toBe("DELETE");
  });

  it("账号组端点非 2xx 时抛 ApiError", async () => {
    const failing = { ok: false, status: 400 };
    vi.mocked(fetch).mockResolvedValue(
      mockResponse({ error: "bad group" }, failing)
    );
    const cases = [
      () => listAccountGroups(),
      () => createAccountGroup("g"),
      () => renameAccountGroup(1, "g"),
      () => deleteAccountGroup(1),
      () => addAccountGroupToken(1, 2),
      () => removeAccountGroupToken(1, 2),
    ];
    for (const call of cases) {
      await expect(call()).rejects.toMatchObject({
        name: "ApiError",
        status: 400,
        message: "bad group",
      });
    }
  });

  it("listFailovers GET /api/account/failover 并透传返回值", async () => {
    const data = {
      failovers: [
        {
          instance_id: 10,
          user_id: 2,
          group_id: 1,
          token_id: 3,
          enabled: true,
          threshold: 3,
          interval_seconds: 300,
          fail_count: 0,
          last_status: "ok",
          last_detail: "",
          last_check_at: "2026-01-01T00:00:00Z",
          last_task_id: "",
          replaced_by_instance_id: 0,
          updated_at: "2026-01-01T00:00:00Z",
        },
      ],
    };
    vi.mocked(fetch).mockResolvedValue(mockResponse(data));
    await expect(listFailovers()).resolves.toEqual(data);
    const [url, opts] = await lastCall();
    expect(url).toBe("/api/account/failover");
    expect(opts.method).toBeUndefined();
  });

  it("setInstanceFailover PUT 编码 instanceId 并带 payload", async () => {
    vi.mocked(fetch).mockResolvedValue(mockResponse({ ok: true }));
    const payload = {
      token_id: 3,
      group_id: 1,
      enabled: true,
      threshold: 3,
      interval_seconds: 300,
    };
    await expect(setInstanceFailover("i/1", payload)).resolves.toEqual({ ok: true });
    const [url, opts] = await lastCall();
    expect(url).toBe("/api/account/failover/i%2F1");
    expect(opts.method).toBe("PUT");
    expect(JSON.parse(opts.body)).toEqual(payload);
  });

  it("deleteInstanceFailover DELETE 编码 instanceId", async () => {
    vi.mocked(fetch).mockResolvedValue(mockResponse({ ok: true }));
    await expect(deleteInstanceFailover("i 1")).resolves.toEqual({ ok: true });
    const [url, opts] = await lastCall();
    expect(url).toBe("/api/account/failover/i%201");
    expect(opts.method).toBe("DELETE");
    expect(opts.body).toBeUndefined();
  });

  it("替换端点非 2xx 时抛 ApiError", async () => {
    vi.mocked(fetch).mockResolvedValue(
      mockResponse({ error: "bad failover" }, { ok: false, status: 400 })
    );
    const cases = [
      () => listFailovers(),
      () => setInstanceFailover(1, { enabled: true }),
      () => deleteInstanceFailover(1),
    ];
    for (const call of cases) {
      await expect(call()).rejects.toMatchObject({
        name: "ApiError",
        status: 400,
        message: "bad failover",
      });
    }
  });
});
