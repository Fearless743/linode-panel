/* eslint-disable react/prop-types */
// usePolling Hook 单元测试：mock 定时器验证 立即执行 / interval / 停止 / 卸载清理。
// node 环境无 DOM：使用极简容器桩 + react-dom/client 的 createRoot 挂载一个返回 null 的
// 探针组件，使 useEffect/useState 在真实 React 中运行。
// 通过 vi.hoisted 在 import react-dom 之前 mock MessageChannel，让 React scheduler 的
// 回调走微任务，配合 async act 完整 flush（避免 node 环境下 scheduler 宏任务未跑导致的
// "Should not already be working"）。
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.hoisted(() => {
  class MessageChannelMock {
    constructor() {
      this.port1 = { onmessage: null };
      this.port2 = {};
      this.port2.postMessage = () => {
        queueMicrotask(() => {
          if (this.port1.onmessage) this.port1.onmessage(null);
        });
      };
    }
  }
  globalThis.MessageChannel = MessageChannelMock;
});

import { createRoot } from "react-dom/client";
import { act } from "react";
import { usePolling } from "../usePolling";

// 让 React act 在非测试环境下正常工作
globalThis.IS_REACT_ACT_ENVIRONMENT = true;
// node 环境无 window/document，提供最小全局桩供 react-dom 读取
globalThis.window = {
  event: undefined,
  document: { activeElement: null },
  HTMLElement: class HTMLElement {},
  SVGElement: class SVGElement {},
  HTMLIFrameElement: class HTMLIFrameElement {},
};
globalThis.document = {
  nodeType: 9,
  activeElement: null,
  body: null,
  addEventListener: () => {},
  removeEventListener: () => {},
  createElement: () => ({ style: {}, setAttribute: () => {} }),
};

async function mountPolling(initialProps) {
  let result = null;
  const container = {
    nodeType: 1, // ELEMENT_NODE，满足 isValidContainer；事件监听全部 no-op
    tagName: "#root",
    ownerDocument: {
      nodeType: 9,
      addEventListener: () => {},
      removeEventListener: () => {},
    },
    addEventListener: () => {},
    removeEventListener: () => {},
    firstChild: null,
  };
  const root = createRoot(container);
  function Probe(props) {
    result = usePolling(props.fn, props.intervalMs, props.enabled);
    return null;
  }
  async function renderProps(props) {
    await act(async () => {
      root.render(<Probe {...props} />);
    });
  }
  await renderProps(initialProps);
  return {
    get result() {
      return result;
    },
    rerender: renderProps,
    async unmount() {
      await act(async () => {
        root.unmount();
      });
    },
  };
}

describe("usePolling", () => {
  beforeEach(() => {
    // 仅 fake interval 相关，保留真实 setTimeout 供 React scheduler 调度
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("enabled 时立即执行一次并按 intervalMs 循环", async () => {
    const fn = vi.fn();
    const mounted = await mountPolling({ fn, intervalMs: 1000, enabled: true });
    expect(fn).toHaveBeenCalledTimes(1);
    expect(mounted.result.running).toBe(true);

    act(() => {
      vi.advanceTimersByTime(1000);
    });
    expect(fn).toHaveBeenCalledTimes(2);

    act(() => {
      vi.advanceTimersByTime(1000);
    });
    expect(fn).toHaveBeenCalledTimes(3);
  });

  it("enabled=false 时不执行且 running=false", async () => {
    const fn = vi.fn();
    const mounted = await mountPolling({ fn, intervalMs: 1000, enabled: false });
    expect(fn).not.toHaveBeenCalled();
    expect(mounted.result.running).toBe(false);

    act(() => {
      vi.advanceTimersByTime(5000);
    });
    expect(fn).not.toHaveBeenCalled();
  });

  it("enabled true→false 停止轮询且 running=false", async () => {
    const fn = vi.fn();
    const mounted = await mountPolling({ fn, intervalMs: 1000, enabled: true });
    expect(fn).toHaveBeenCalledTimes(1);

    await mounted.rerender({ fn, intervalMs: 1000, enabled: false });
    expect(mounted.result.running).toBe(false);

    const callsAfterStop = fn.mock.calls.length;
    act(() => {
      vi.advanceTimersByTime(5000);
    });
    expect(fn).toHaveBeenCalledTimes(callsAfterStop);
  });

  it("卸载时清理定时器，不再执行", async () => {
    const fn = vi.fn();
    const mounted = await mountPolling({ fn, intervalMs: 1000, enabled: true });
    expect(fn).toHaveBeenCalledTimes(1);

    await mounted.unmount();
    const callsAfterUnmount = fn.mock.calls.length;
    act(() => {
      vi.advanceTimersByTime(5000);
    });
    expect(fn).toHaveBeenCalledTimes(callsAfterUnmount);
  });
});
