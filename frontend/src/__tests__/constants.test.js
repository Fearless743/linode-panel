// 常量集中管理单元测试：标签、状态集合、间隔值、API 路径。
import { describe, it, expect } from "vitest";
import {
  PERM_LABELS,
  STATUS_LABELS,
  TRANSITION_STATES,
  POLL_INTERVAL_MS,
  SSH_TIMEOUT_MS,
  TASK_POLL_INTERVAL_MS,
  AGENT_POLL_INTERVAL_MS,
  ACTION_LABELS,
  COMMAND_TYPE_LABELS,
  COMMAND_STATUS_LABELS,
  API_PATHS,
} from "../constants";

describe("PERM_LABELS", () => {
  it("包含 5 个权限且为 [key,label] 结构", () => {
    expect(PERM_LABELS).toHaveLength(5);
    const keys = PERM_LABELS.map(([k]) => k);
    expect(keys).toEqual(["view", "power", "rebuild", "script", "admin"]);
    PERM_LABELS.forEach(([k, label]) => {
      expect(typeof k).toBe("string");
      expect(typeof label).toBe("string");
      expect(label.length).toBeGreaterThan(0);
    });
  });
});

describe("STATUS_LABELS", () => {
  it("覆盖 running/offline/booting/rebooting/shutting_down", () => {
    expect(Object.keys(STATUS_LABELS).sort()).toEqual(
      ["booting", "offline", "rebooting", "running", "shutting_down"].sort()
    );
    expect(STATUS_LABELS.running).toBe("运行中");
    expect(STATUS_LABELS.offline).toBe("已关机");
  });
});

describe("TRANSITION_STATES", () => {
  it("是包含关键过渡态的 Set", () => {
    expect(TRANSITION_STATES).toBeInstanceOf(Set);
    expect(TRANSITION_STATES.has("booting")).toBe(true);
    expect(TRANSITION_STATES.has("rebooting")).toBe(true);
    expect(TRANSITION_STATES.has("shutting_down")).toBe(true);
    expect(TRANSITION_STATES.has("provisioning")).toBe(true);
    expect(TRANSITION_STATES.has("running")).toBe(false);
    expect(TRANSITION_STATES.has("offline")).toBe(false);
  });
});

describe("间隔 / 超时", () => {
  it("轮询与超时值为正数且符合约定", () => {
    expect(POLL_INTERVAL_MS).toBe(3000);
    expect(SSH_TIMEOUT_MS).toBe(5 * 60 * 1000);
    expect(TASK_POLL_INTERVAL_MS).toBe(2000);
    expect(AGENT_POLL_INTERVAL_MS).toBe(5000);
  });
});

describe("ACTION_LABELS / COMMAND 标签", () => {
  it("操作标签完整", () => {
    expect(ACTION_LABELS).toEqual({ boot: "开机", shutdown: "关机", reboot: "重启" });
  });

  it("命令类型标签完整", () => {
    expect(COMMAND_TYPE_LABELS).toEqual({ list: "列出实例", power: "电源操作", rebuild: "重建" });
  });

  it("命令状态标签完整", () => {
    expect(COMMAND_STATUS_LABELS).toEqual({
      queued: "排队中",
      in_flight: "执行中",
      completed: "已完成",
    });
  });
});

describe("API_PATHS", () => {
  it("路径常量与函数", () => {
    expect(API_PATHS.authMe).toBe("/api/auth/me");
    expect(API_PATHS.authLogin).toBe("/api/auth/login");
    expect(API_PATHS.authLogout).toBe("/api/auth/logout");
    expect(API_PATHS.instances).toBe("/api/instances");
    expect(API_PATHS.instanceStatus("i1")).toBe("/api/instances/i1/status");
    expect(API_PATHS.sshKeys).toBe("/api/ssh-keys");
    expect(API_PATHS.rebuildTasks).toBe("/api/rebuild/tasks");
    expect(API_PATHS.users).toBe("/api/users");
    expect(API_PATHS.groups).toBe("/api/groups");
    expect(API_PATHS.agents).toBe("/api/agents");
    expect(API_PATHS.agentCommands).toBe("/api/agent/commands");
    expect(API_PATHS.agentRegcode).toBe("/api/agents/regcode");
  });
});
