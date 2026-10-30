// ---------- 账号组状态聚合（纯函数） ----------
// 账号组卡片需要把「按实例」的替换配置（GET /api/account/failover 的 failovers）
// 聚合到「按成员账号」的展示状态。本模块无副作用、不修改入参、对空值健壮。
import { describeFailover } from "./accountGroups.js";

// 状态严重度：数值越大越严重。muted（未检测）< ok < warn < danger。
// 聚合时取成员账号名下所有实例中「最严重」的一条状态。
const TONE_RANK = { muted: 0, ok: 1, warn: 2, danger: 3 };

// 组成员账号列表：group.token_ids 映射到 tokens 中的账号信息。
// - 保持 group.token_ids 顺序（后端已按 id 升序返回）
// - group.token_ids 为空/非数组 -> []
// - 找不到对应 token 的 id 仍保留，label 回退 "#id"，known=false
// 返回 [{ id, label, known }]，不修改入参。
export function groupMembers(group, tokens) {
  const ids = Array.isArray(group && group.token_ids) ? group.token_ids : [];
  const list = Array.isArray(tokens) ? tokens : [];
  const byId = new Map(
    list.filter((t) => t && t.id != null).map((t) => [String(t.id), t])
  );
  return ids
    .filter((id) => id != null)
    .map((id) => {
      const hit = byId.get(String(id));
      return {
        id,
        label: hit ? hit.label || "（未命名）" : "#" + id,
        known: !!hit,
      };
    });
}

// 取出某组某账号名下的替换配置（按 group_id + token_id 匹配）。
// groupId/tokenId 用字符串比较，兼容数字/字符串 id。非数组返回 []。
export function failoversForMember(failovers, groupId, tokenId) {
  if (!Array.isArray(failovers)) return [];
  return failovers.filter(
    (f) =>
      f &&
      String(f.group_id) === String(groupId) &&
      String(f.token_id) === String(tokenId)
  );
}

// 聚合某成员账号名下所有实例的替换状态，取最严重的一条。
// 返回 { label, tone, count }：
// - 无配置：{ label: "未检测", tone: "muted", count: 0 }
// - 未知/空状态一律回退「未检测」
// - count 为该账号已配置替换的实例数
export function aggregateMemberStatus(failovers, groupId, tokenId) {
  const entries = failoversForMember(failovers, groupId, tokenId);
  if (entries.length === 0) return { label: "未检测", tone: "muted", count: 0 };

  let best = null;
  let bestRank = -1;
  for (const entry of entries) {
    const described = describeFailover(entry);
    const rank = Object.hasOwn(TONE_RANK, described.tone)
      ? TONE_RANK[described.tone]
      : 0;
    if (rank > bestRank) {
      bestRank = rank;
      best = described;
    }
  }
  const tone = best ? best.tone : "muted";
  return {
    // 未检测（muted）统一显示「未检测」，不展示后端的原始未知值。
    label: tone === "muted" ? "未检测" : best.label,
    tone,
    count: entries.length,
  };
}
