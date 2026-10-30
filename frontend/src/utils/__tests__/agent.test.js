// agentInstallCommand 单元测试。
import { describe, it, expect } from "vitest";
import { agentInstallCommand } from "../agent";

describe("agentInstallCommand", () => {
  it("拼接 panel / token / code 一键安装命令", () => {
    const cmd = agentInstallCommand({
      origin: "https://panel.example.com",
      token: "tok123",
      code: "regcode456",
    });
    expect(cmd).toBe(
      "linode-panel-agent --panel https://panel.example.com --token tok123 --code regcode456"
    );
  });

  it("去掉 origin 末尾斜杠", () => {
    const cmd = agentInstallCommand({ origin: "http://127.0.0.1:8787/", code: "c" });
    expect(cmd).toBe("linode-panel-agent --panel http://127.0.0.1:8787 --code c");
  });

  it("缺少 token 时退化为 --code 注册命令", () => {
    const cmd = agentInstallCommand({ origin: "https://p.example.com", code: "abc" });
    expect(cmd).toBe("linode-panel-agent --panel https://p.example.com --code abc");
    expect(cmd).not.toContain("--token");
  });

  it("含特殊字符的值被 shell 引用", () => {
    const cmd = agentInstallCommand({
      origin: "https://panel.example.com",
      token: "tok 123",
      code: "reg'code",
    });
    expect(cmd).toContain("--token 'tok 123'");
    expect(cmd).toContain("--code 'reg'\\''code'");
  });

  it("缺少 origin 时返回空字符串", () => {
    expect(agentInstallCommand({ token: "t", code: "c" })).toBe("");
    expect(agentInstallCommand()).toBe("");
  });
});
