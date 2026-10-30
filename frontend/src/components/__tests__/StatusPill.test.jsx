// StatusPill 组件单元测试：使用 react-dom/server 的 renderToStaticMarkup 断言 class 与文本。
import { describe, it, expect } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import StatusPill from "../StatusPill";

describe("StatusPill", () => {
  it("running 渲染绿色 class 与中文标签", () => {
    const html = renderToStaticMarkup(<StatusPill status="running" />);
    expect(html).toContain("status-running");
    expect(html).not.toContain("status-transition");
    expect(html).toContain("运行中");
  });

  it("offline 渲染灰色 class 与中文标签", () => {
    const html = renderToStaticMarkup(<StatusPill status="offline" />);
    expect(html).toContain("status-offline");
    expect(html).toContain("已关机");
  });

  it("过渡态（booting/rebooting/shutting_down）渲染 transition class 且含 spinner", () => {
    for (const status of ["booting", "rebooting", "shutting_down"]) {
      const html = renderToStaticMarkup(<StatusPill status={status} />);
      expect(html).toContain("status-transition");
      expect(html).toContain("spinner");
    }
  });

  it("未知状态渲染 default class 且回退显示原始 status", () => {
    const html = renderToStaticMarkup(<StatusPill status="weird" />);
    expect(html).toContain("status-unknown");
    expect(html).toContain("weird");
    expect(html).not.toContain("spinner");
  });

  it("缺失 status 渲染 default class", () => {
    const html = renderToStaticMarkup(<StatusPill />);
    expect(html).toContain("status-unknown");
  });
});
