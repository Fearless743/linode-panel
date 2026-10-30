// 剪贴板工具单元测试。
// node 环境无 navigator/document/window，测试中通过 defineProperty 注入桩。
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { copyText } from "../clipboard";

function setGlobal(name, value) {
  Object.defineProperty(globalThis, name, { value, configurable: true, writable: true });
}

function makeDocumentStub({ execOk = true } = {}) {
  const textarea = {
    value: "",
    style: {},
    setAttribute: vi.fn(),
    select: vi.fn(),
  };
  const documentStub = {
    createElement: vi.fn(() => textarea),
    body: { appendChild: vi.fn(), removeChild: vi.fn() },
    execCommand: vi.fn(() => execOk),
  };
  return { documentStub, textarea };
}

describe("copyText", () => {
  let original;

  beforeEach(() => {
    original = {
      navigator: Object.getOwnPropertyDescriptor(globalThis, "navigator"),
      document: Object.getOwnPropertyDescriptor(globalThis, "document"),
      window: Object.getOwnPropertyDescriptor(globalThis, "window"),
    };
  });

  afterEach(() => {
    for (const [name, desc] of Object.entries(original)) {
      if (desc) Object.defineProperty(globalThis, name, desc);
      else delete globalThis[name];
    }
  });

  it("navigator.clipboard 可用时优先使用 writeText", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    setGlobal("navigator", { clipboard: { writeText } });
    const done = vi.fn();
    copyText("hello", done);
    await vi.waitFor(() => expect(done).toHaveBeenCalledTimes(1));
    expect(writeText).toHaveBeenCalledWith("hello");
  });

  it("writeText 失败时回退到 execCommand", async () => {
    const writeText = vi.fn().mockRejectedValue(new Error("denied"));
    setGlobal("navigator", { clipboard: { writeText } });
    const { documentStub, textarea } = makeDocumentStub({ execOk: true });
    setGlobal("document", documentStub);
    setGlobal("window", { alert: vi.fn() });
    const done = vi.fn();
    copyText("fallback me", done);
    // 等待 rejection handler 的微任务执行
    await Promise.resolve();
    await Promise.resolve();
    expect(documentStub.execCommand).toHaveBeenCalledWith("copy");
    expect(textarea.value).toBe("fallback me");
    expect(done).toHaveBeenCalledTimes(1);
  });

  it("clipboard 不可用时使用 execCommand 回退", () => {
    setGlobal("navigator", {});
    const { documentStub, textarea } = makeDocumentStub({ execOk: true });
    setGlobal("document", documentStub);
    setGlobal("window", { alert: vi.fn() });
    const done = vi.fn();
    copyText("no clipboard", done);
    expect(documentStub.execCommand).toHaveBeenCalledWith("copy");
    expect(textarea.value).toBe("no clipboard");
    expect(done).toHaveBeenCalledTimes(1);
  });

  it("execCommand 失败时弹出告警且不回调", () => {
    setGlobal("navigator", {});
    const { documentStub } = makeDocumentStub({ execOk: false });
    const alert = vi.fn();
    setGlobal("document", documentStub);
    setGlobal("window", { alert });
    const done = vi.fn();
    copyText("fail copy", done);
    expect(alert).toHaveBeenCalledTimes(1);
    expect(alert.mock.calls[0][0]).toContain("fail copy");
    expect(done).not.toHaveBeenCalled();
  });
});
