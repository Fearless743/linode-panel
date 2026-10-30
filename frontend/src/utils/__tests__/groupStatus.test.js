// 账号组状态聚合单元测试：成员映射、成员配置过滤、最严重状态聚合。
import { describe, it, expect } from "vitest";
import {
  groupMembers,
  failoversForMember,
  aggregateMemberStatus,
} from "../groupStatus";

describe("groupMembers", () => {
  const tokens = [
    { id: 1, label: "A" },
    { id: 2, label: "B" },
    { id: 3, label: "" },
  ];

  it("按 token_ids 顺序映射 label", () => {
    expect(groupMembers({ token_ids: [3, 1, 2] }, tokens)).toEqual([
      { id: 3, label: "（未命名）", known: true },
      { id: 1, label: "A", known: true },
      { id: 2, label: "B", known: true },
    ]);
  });

  it("空 token_ids / 非数组 -> []", () => {
    expect(groupMembers({ token_ids: [] }, tokens)).toEqual([]);
    expect(groupMembers({}, tokens)).toEqual([]);
    expect(groupMembers(null, tokens)).toEqual([]);
    expect(groupMembers({ token_ids: [1] }, null)).toEqual([
      { id: 1, label: "#1", known: false },
    ]);
  });

  it("找不到的 token 保留并标记 known=false", () => {
    expect(groupMembers({ token_ids: [1, 99] }, tokens)).toEqual([
      { id: 1, label: "A", known: true },
      { id: 99, label: "#99", known: false },
    ]);
  });

  it("不修改入参", () => {
    const group = { token_ids: [2, 1] };
    const snap = JSON.parse(JSON.stringify(group));
    groupMembers(group, tokens);
    expect(group).toEqual(snap);
  });

  it("跳过 null/undefined 的 id", () => {
    expect(groupMembers({ token_ids: [null, 1, undefined] }, tokens)).toEqual([
      { id: 1, label: "A", known: true },
    ]);
  });
});

describe("failoversForMember", () => {
  const failovers = [
    { instance_id: 10, group_id: 1, token_id: 3, last_status: "ok" },
    { instance_id: 11, group_id: 1, token_id: 3, last_status: "blocked" },
    { instance_id: 12, group_id: 1, token_id: 4, last_status: "banned" },
    { instance_id: 13, group_id: 2, token_id: 3, last_status: "error" },
  ];

  it("按 group_id + token_id 精确匹配", () => {
    expect(failoversForMember(failovers, 1, 3).map((f) => f.instance_id)).toEqual([10, 11]);
  });

  it("兼容字符串 id", () => {
    expect(failoversForMember(failovers, "1", "3").map((f) => f.instance_id)).toEqual([10, 11]);
  });

  it("非数组返回 []", () => {
    expect(failoversForMember(null, 1, 3)).toEqual([]);
    expect(failoversForMember(undefined, 1, 3)).toEqual([]);
  });
});

describe("aggregateMemberStatus", () => {
  it("无配置回退未检测", () => {
    expect(aggregateMemberStatus([], 1, 3)).toEqual({
      label: "未检测",
      tone: "muted",
      count: 0,
    });
    expect(aggregateMemberStatus(null, 1, 3)).toEqual({
      label: "未检测",
      tone: "muted",
      count: 0,
    });
  });

  it("取最严重状态（danger > warn > ok）并统计数量", () => {
    const failovers = [
      { instance_id: 1, group_id: 1, token_id: 3, last_status: "ok" },
      { instance_id: 2, group_id: 1, token_id: 3, last_status: "blocked" },
      { instance_id: 3, group_id: 1, token_id: 3, last_status: "banned" },
    ];
    expect(aggregateMemberStatus(failovers, 1, 3)).toEqual({
      label: "账号封禁",
      tone: "danger",
      count: 3,
    });
  });

  it("ok 优先于未检测（muted）", () => {
    const failovers = [
      { instance_id: 1, group_id: 1, token_id: 3, last_status: "idle" },
      { instance_id: 2, group_id: 1, token_id: 3, last_status: "ok" },
    ];
    expect(aggregateMemberStatus(failovers, 1, 3)).toEqual({
      label: "正常",
      tone: "ok",
      count: 2,
    });
  });

  it("未知状态回退「未检测」", () => {
    const failovers = [
      { instance_id: 1, group_id: 1, token_id: 3, last_status: "totally-unknown" },
    ];
    expect(aggregateMemberStatus(failovers, 1, 3)).toEqual({
      label: "未检测",
      tone: "muted",
      count: 1,
    });
  });

  it("只聚合匹配的组与账号", () => {
    const failovers = [
      { instance_id: 1, group_id: 1, token_id: 3, last_status: "ok" },
      { instance_id: 2, group_id: 2, token_id: 3, last_status: "banned" },
      { instance_id: 3, group_id: 1, token_id: 4, last_status: "banned" },
    ];
    expect(aggregateMemberStatus(failovers, 1, 3)).toEqual({
      label: "正常",
      tone: "ok",
      count: 1,
    });
  });

  it("同级取先出现的一条", () => {
    const failovers = [
      { instance_id: 1, group_id: 1, token_id: 3, last_status: "blocked" },
      { instance_id: 2, group_id: 1, token_id: 3, last_status: "no_balance" },
    ];
    expect(aggregateMemberStatus(failovers, 1, 3)).toMatchObject({
      tone: "warn",
      label: "疑似被墙",
      count: 2,
    });
  });
});
