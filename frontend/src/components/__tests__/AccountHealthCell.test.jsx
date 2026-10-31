// AccountHealthCell 组件单元测试。
import { describe, it, expect } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import AccountHealthCell from "../accounts/AccountHealthCell";

describe("AccountHealthCell", () => {
  it("正常状态渲染绿色 pill 与余额", () => {
    const html = renderToStaticMarkup(
      <AccountHealthCell token={{ health_status: "ok", balance: -2.5, balance_uninvoiced: 0.5 }} />
    );
    expect(html).toContain("status-running");
    expect(html).toContain("正常");
    expect(html).toContain("欠款 -2.50");
    expect(html).toContain("未出账 0.50");
  });

  it("余额不足渲染黄色 pill", () => {
    const html = renderToStaticMarkup(
      <AccountHealthCell token={{ health_status: "no_balance", balance: 3.0, balance_uninvoiced: 0 }} />
    );
    expect(html).toContain("status-transition");
    expect(html).toContain("余额不足");
  });

  it("封禁渲染红色 pill", () => {
    const html = renderToStaticMarkup(
      <AccountHealthCell token={{ health_status: "banned" }} />
    );
    expect(html).toContain("status-danger");
    expect(html).toContain("封禁");
  });

  it("检测失败渲染灰色 pill", () => {
    const html = renderToStaticMarkup(
      <AccountHealthCell token={{ health_status: "error", health_detail: "查询账号失败: timeout" }} />
    );
    expect(html).toContain("status-unknown");
    expect(html).toContain("检测失败");
    expect(html).toContain("查询账号失败");
  });

  it("未检测（无 health_status）渲染「未检测」", () => {
    const html = renderToStaticMarkup(<AccountHealthCell token={{}} />);
    expect(html).toContain("status-unknown");
    expect(html).toContain("未检测");
  });

  it("未探测时余额显示 —", () => {
    const html = renderToStaticMarkup(
      <AccountHealthCell token={{ health_status: "banned" }} />
    );
    expect(html).toContain("—");
  });
});
