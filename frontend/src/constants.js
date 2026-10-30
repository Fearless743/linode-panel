// ---------- 常量定义：集中管理共享常量 ----------
// 从各页面/组件抽取的权限、状态、轮询间隔、操作标签、命令状态、开机脚本与 API 路径。

// ---------- 权限 ----------
export const PERM_LABELS = [
  ["view", "查看"],
  ["power", "开关机"],
  ["rebuild", "重建"],
  ["script", "脚本"],
  ["admin", "管理"],
];

// ---------- 实例状态 ----------
export const STATUS_LABELS = {
  running: "运行中",
  offline: "已关机",
  booting: "启动中",
  rebooting: "重启中",
  shutting_down: "关机中",
};

export const TRANSITION_STATES = new Set([
  "booting",
  "rebooting",
  "shutting_down",
  "provisioning",
  "rebuilding",
  "cloning",
  "restoring",
  "migrating",
]);

// ---------- 间隔 / 超时 ----------
export const POLL_INTERVAL_MS = 3000;
export const SSH_TIMEOUT_MS = 5 * 60 * 1000;
export const TASK_POLL_INTERVAL_MS = 2000;
export const AGENT_POLL_INTERVAL_MS = 5000;

// ---------- 操作标签 ----------
export const ACTION_LABELS = { boot: "开机", shutdown: "关机", reboot: "重启" };

// ---------- 命令 / 任务状态 ----------
export const COMMAND_TYPE_LABELS = { list: "列出实例", power: "电源操作", rebuild: "重建" };
export const COMMAND_STATUS_LABELS = {
  queued: "排队中",
  in_flight: "执行中",
  completed: "已完成",
};

// ---------- API 路径 ----------
export const API_PATHS = {
  authMe: "/api/auth/me",
  authLogin: "/api/auth/login",
  authLogout: "/api/auth/logout",
  instances: "/api/instances",
  instanceStatus: (id) => "/api/instances/" + id + "/status",
  sshKeys: "/api/ssh-keys",
  rebuildTasks: "/api/rebuild/tasks",
  users: "/api/users",
  groups: "/api/groups",
  agents: "/api/agents",
  agentCommands: "/api/agent/commands",
  agentRegcode: "/api/agents/regcode",
};
