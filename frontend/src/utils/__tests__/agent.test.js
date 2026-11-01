// agentInstallCommand 单元测试。
import { describe, it, expect } from "vitest";
import { agentInstallCommand } from "../agent";

describe("agentInstallCommand", () => {
  it("拼接 panel / code 一键安装命令", () => {
    const cmd = agentInstallCommand({
      origin: "https://panel.example.com",
      code: "regcode456",
    });
    expect(cmd).toBe(
      "linode-panel-agent --panel https://panel.example.com --code regcode456"
    );
  });

  it("去掉 origin 末尾斜杠", () => {
    const cmd = agentInstallCommand({ origin: "http://127.0.0.1:8787/", code: "c" });
    expect(cmd).toBe("linode-panel-agent --panel http://127.0.0.1:8787 --code c");
  });

  it("缺少 code 时只有 --panel", () => {
    const cmd = agentInstallCommand({ origin: "https://p.example.com" });
    expect(cmd).toBe("linode-panel-agent --panel https://p.example.com");
  });

  it("含特殊字符的值被 shell 引用", () => {
    const cmd = agentInstallCommand({
      origin: "https://panel.example.com",
      code: "reg'code",
    });
    expect(cmd).toContain("--code 'reg'\\''code'");
  });

  it("缺少 origin 时返回空字符串", () => {
    expect(agentInstallCommand({ code: "c" })).toBe("");
    expect(agentInstallCommand()).toBe("");
  });
});
