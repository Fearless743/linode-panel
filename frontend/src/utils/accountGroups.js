// ---------- 账号组纯工具与常量 ----------
// 「账号组」= 把多个 Linode 账号（PAT，后端 Token）绑定成组；
// 组内某账号的实例被封号或余额不足时，用相同配置在组内另一账号重建替换。
// 本模块只提供纯函数/常量：无副作用、不修改入参、对空值健壮，便于 UI 与后续任务复用。

// 余额告警阈值：余额 <= 该值视为欠款/不足（正数表示欠款）。
export const BALANCE_ALERT_THRESHOLD = 0;

// 失败切换状态 -> 展示文案与色调。
// cfg 形如 { fail_count, last_status, last_detail, ... }。
// 返回 { label, tone }，tone ∈ "ok" | "warn" | "danger" | "muted"。
const FAILOVER_STATUS_MAP = {
  "": { label: "未检测", tone: "muted" },
  idle: { label: "未检测", tone: "muted" },
  ok: { label: "正常", tone: "ok" },
  blocked: { label: "疑似被墙", tone: "warn" },
  no_balance: { label: "余额不足", tone: "warn" },
  banned: { label: "账号封禁", tone: "danger" },
  rebuilding: { label: "替换中", tone: "warn" },
  error: { label: "检测异常", tone: "danger" },
};

export function describeFailover(cfg) {
  const status = cfg && cfg.last_status != null ? String(cfg.last_status) : "";
  // 只认自有键，避免命中 Object.prototype 上的继承属性（如 toString/constructor/__proto__）。
  const hit = Object.hasOwn(FAILOVER_STATUS_MAP, status) ? FAILOVER_STATUS_MAP[status] : null;
  if (hit) return { ...hit };
  // 未知值：回退 muted，label 用原文，空则用「未知」。
  return { label: status || "未知", tone: "muted" };
}

// 组列表按 name 排序（localeCompare），不修改入参，返回新数组。
export function sortAccountGroups(groups) {
  if (!Array.isArray(groups)) return [];
  return [...groups].sort((a, b) =>
    String((a && a.name) || "").localeCompare(String((b && b.name) || ""))
  );
}

// 从组内候选账号里挑出可用于替换的账号。
// group: { token_ids: number[] }, tokens: [{ id, label }]。
// excludeTokenId: 当前故障账号 id（不可选自己）。
// 返回按 id 升序的候选 token 数组（[{id,label}]）。
// tokens 中不存在于 group.token_ids 的 id 会被忽略。
export function replacementCandidates(group, tokens, excludeTokenId) {
  if (!Array.isArray(tokens)) return [];
  const ids = Array.isArray(group && group.token_ids) ? group.token_ids : [];
  const wanted = new Set(ids);
  return tokens
    .filter((t) => t && wanted.has(t.id) && t.id !== excludeTokenId)
    .map((t) => ({ id: t.id, label: t.label }))
    .sort((a, b) => a.id - b.id);
}

// 组内是否有可替换账号（用于 UI 禁用态）。
export function canFailover(group, tokens, excludeTokenId) {
  return replacementCandidates(group, tokens, excludeTokenId).length > 0;
}

// 余额展示：仅接受 number 类型，保留两位小数；其余（null/undefined/NaN/非有限数/空串/布尔/数组等）-> "—"。
export function formatBalance(n) {
  if (typeof n !== "number" || !Number.isFinite(n)) return "—";
  return n.toFixed(2);
}
