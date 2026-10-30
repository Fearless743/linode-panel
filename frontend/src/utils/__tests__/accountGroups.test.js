// 账号组纯工具单元测试：状态映射、排序、替换候选、余额格式化与可替换判断。
import { describe, it, expect } from "vitest";
import {
  describeFailover,
  sortAccountGroups,
  replacementCandidates,
  canFailover,
  formatBalance,
  BALANCE_ALERT_THRESHOLD,
} from "../accountGroups";

describe("describeFailover", () => {
  it("映射各已知状态到文案与色调", () => {
    expect(describeFailover({ last_status: "" })).toEqual({ label: "未检测", tone: "muted" });
    expect(describeFailover({ last_status: "idle" })).toEqual({ label: "未检测", tone: "muted" });
    expect(describeFailover({ last_status: "ok" })).toEqual({ label: "正常", tone: "ok" });
    expect(describeFailover({ last_status: "blocked" })).toEqual({ label: "疑似被墙", tone: "warn" });
    expect(describeFailover({ last_status: "no_balance" })).toEqual({ label: "余额不足", tone: "warn" });
    expect(describeFailover({ last_status: "banned" })).toEqual({ label: "账号封禁", tone: "danger" });
    expect(describeFailover({ last_status: "rebuilding" })).toEqual({ label: "替换中", tone: "warn" });
    expect(describeFailover({ last_status: "error" })).toEqual({ label: "检测异常", tone: "danger" });
  });

  it("缺少 last_status 视为未检测", () => {
    expect(describeFailover({})).toEqual({ label: "未检测", tone: "muted" });
    expect(describeFailover(null)).toEqual({ label: "未检测", tone: "muted" });
    expect(describeFailover(undefined)).toEqual({ label: "未检测", tone: "muted" });
  });

  it("未知状态回退 muted 且 label 用原文", () => {
    expect(describeFailover({ last_status: "weird" })).toEqual({ label: "weird", tone: "muted" });
  });

  it("status 为 null 时按未检测处理，非空未知值用原文", () => {
    expect(describeFailover({ last_status: null })).toEqual({ label: "未检测", tone: "muted" });
    expect(describeFailover({ last_status: 0 })).toEqual({ label: "0", tone: "muted" });
  });
});

describe("sortAccountGroups", () => {
  it("按 name 升序排序且不修改入参", () => {
    const groups = [
      { id: 1, name: "zeta" },
      { id: 2, name: "alpha" },
      { id: 3, name: "beta" },
    ];
    const snapshot = JSON.parse(JSON.stringify(groups));
    const sorted = sortAccountGroups(groups);
    expect(sorted.map((g) => g.name)).toEqual(["alpha", "beta", "zeta"]);
    expect(groups).toEqual(snapshot); // 入参未被修改
    expect(sorted).not.toBe(groups); // 返回新数组
  });

  it("排序稳定：同名保持原相对顺序", () => {
    const groups = [
      { id: 1, name: "same" },
      { id: 2, name: "same" },
      { id: 3, name: "aaa" },
    ];
    expect(sortAccountGroups(groups).map((g) => g.id)).toEqual([3, 1, 2]);
  });

  it("空值/非数组返回空数组且不抛异常", () => {
    expect(sortAccountGroups(null)).toEqual([]);
    expect(sortAccountGroups(undefined)).toEqual([]);
    expect(sortAccountGroups("nope")).toEqual([]);
  });
});

describe("replacementCandidates", () => {
  const group = { token_ids: [3, 1, 2] };
  const tokens = [
    { id: 1, label: "A" },
    { id: 2, label: "B" },
    { id: 3, label: "C" },
    { id: 9, label: "无关" },
  ];

  it("排除自己并按 id 升序返回候选", () => {
    expect(replacementCandidates(group, tokens, 2)).toEqual([
      { id: 1, label: "A" },
      { id: 3, label: "C" },
    ]);
  });

  it("忽略 tokens 中不存在的组内 id", () => {
    expect(replacementCandidates({ token_ids: [1, 999] }, tokens, null)).toEqual([
      { id: 1, label: "A" },
    ]);
  });

  it("不修改入参", () => {
    const g = { token_ids: [3, 1] };
    const t = [{ id: 1, label: "A" }, { id: 3, label: "C" }];
    const gSnap = JSON.parse(JSON.stringify(g));
    const tSnap = JSON.parse(JSON.stringify(t));
    replacementCandidates(g, t, null);
    expect(g).toEqual(gSnap);
    expect(t).toEqual(tSnap);
  });

  it("空值健壮", () => {
    expect(replacementCandidates(null, null, 1)).toEqual([]);
    expect(replacementCandidates(group, null, 1)).toEqual([]);
    expect(replacementCandidates(null, tokens, 1)).toEqual([]);
  });
});

describe("canFailover", () => {
  const tokens = [
    { id: 1, label: "A" },
    { id: 2, label: "B" },
  ];

  it("组内存在其他账号时为 true", () => {
    expect(canFailover({ token_ids: [1, 2] }, tokens, 1)).toBe(true);
  });

  it("组内只剩自己时为 false", () => {
    expect(canFailover({ token_ids: [1] }, tokens, 1)).toBe(false);
    expect(canFailover({ token_ids: [1] }, tokens, null)).toBe(true);
    expect(canFailover(null, tokens, 1)).toBe(false);
    expect(canFailover({ token_ids: [1, 2] }, null, 1)).toBe(false);
  });
});

describe("formatBalance", () => {
  it("保留两位小数", () => {
    expect(formatBalance(0)).toBe("0.00");
    expect(formatBalance(-1.5)).toBe("-1.50");
    expect(formatBalance(12.345)).toBe("12.35");
    expect(formatBalance(100)).toBe("100.00");
  });

  it("空值与非法数字返回破折号", () => {
    expect(formatBalance(null)).toBe("—");
    expect(formatBalance(undefined)).toBe("—");
    expect(formatBalance(NaN)).toBe("—");
    expect(formatBalance(Infinity)).toBe("—");
    expect(formatBalance("abc")).toBe("—");
  });

  it("余额阈值常量为 0", () => {
    expect(BALANCE_ALERT_THRESHOLD).toBe(0);
  });
});
