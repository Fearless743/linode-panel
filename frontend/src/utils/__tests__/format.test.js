// 日期格式化工具单元测试。
// 断言基于本地时区构造 Date，再取 toISOString() 作为输入，
// 从而与 fmtDate/fmtDateTime 内部 getFullYear/getMonth/...（本地时间）一致，与时区无关。
import { describe, it, expect } from "vitest";
import { fmtDate, fmtDateTime } from "../format";

describe("fmtDate", () => {
  it("空值返回空字符串", () => {
    expect(fmtDate(null)).toBe("");
    expect(fmtDate(undefined)).toBe("");
    expect(fmtDate("")).toBe("");
  });

  it("非法日期返回原字符串", () => {
    expect(fmtDate("not-a-date")).toBe("not-a-date");
    expect(fmtDate("2026-99-99")).toBe("2026-99-99");
  });

  it("合法日期格式化并补零", () => {
    const d = new Date(2026, 0, 5, 4, 7, 9); // 本地 2026-01-05 04:07:09
    expect(fmtDate(d.toISOString())).toBe("2026-01-05 04:07");
  });

  it("两位数无需补零", () => {
    const d = new Date(2026, 11, 25, 14, 30, 45); // 本地 2026-12-25 14:30:45
    expect(fmtDate(d.toISOString())).toBe("2026-12-25 14:30");
  });
});

describe("fmtDateTime", () => {
  it("空值返回空字符串", () => {
    expect(fmtDateTime(null)).toBe("");
    expect(fmtDateTime(undefined)).toBe("");
    expect(fmtDateTime("")).toBe("");
  });

  it("非法日期返回原字符串", () => {
    expect(fmtDateTime("garbage")).toBe("garbage");
  });

  it("包含秒并补零", () => {
    const d = new Date(2026, 0, 5, 4, 7, 9);
    expect(fmtDateTime(d.toISOString())).toBe("2026-01-05 04:07:09");
  });

  it("两位数无需补零", () => {
    const d = new Date(2026, 11, 25, 14, 30, 45);
    expect(fmtDateTime(d.toISOString())).toBe("2026-12-25 14:30:45");
  });
});
