// 地区 / 机型中文名映射单元测试。
import { describe, it, expect } from "vitest";
import {
  REGION_LABELS,
  TYPE_LABELS,
  regionLabel,
  typeLabel,
} from "../linodeLabels";

describe("REGION_LABELS", () => {
  it("常见地区有中文名", () => {
    expect(REGION_LABELS["ap-south"]).toBe("新加坡");
    expect(REGION_LABELS["us-east"]).toBe("纽瓦克（美国·新泽西）");
    expect(REGION_LABELS["eu-west"]).toBe("伦敦（英国）");
    expect(REGION_LABELS["jp-tyo-3"]).toBe("东京 3（日本）");
  });
});

describe("TYPE_LABELS", () => {
  it("常见机型有中文名", () => {
    expect(TYPE_LABELS["g6-standard-2"]).toBe("标准型 4GB");
    expect(TYPE_LABELS["g6-nanode-1"]).toBe("入门型 1GB");
    expect(TYPE_LABELS["g7-highmem-16"]).toBe("高内存型 300GB");
    expect(TYPE_LABELS["g8-dedicated-32-16"]).toBe("独享型 32GB / 16 核");
  });
});

describe("regionLabel / typeLabel", () => {
  it("命中映射时返回中文名", () => {
    expect(regionLabel("ap-south")).toBe("新加坡");
    expect(typeLabel("g6-standard-2")).toBe("标准型 4GB");
  });

  it("未收录 ID 回退到 API label，再回退到原始 ID", () => {
    expect(regionLabel("xx-new", "Somewhere, XX")).toBe("Somewhere, XX");
    expect(regionLabel("xx-new")).toBe("xx-new");
    expect(typeLabel("g9-standard-1", "Linode 1GB")).toBe("Linode 1GB");
    expect(typeLabel("g9-standard-1")).toBe("g9-standard-1");
  });

  it("空值返回 fallback 或空字符串", () => {
    expect(regionLabel("")).toBe("");
    expect(typeLabel(null)).toBe("");
    expect(typeLabel(undefined, "Linode 4GB")).toBe("Linode 4GB");
  });
});
