// 通用轮询 Hook：enabled 时立即执行一次并按 intervalMs 循环执行；
// enabled 变为 false 或组件卸载时清除定时器。

import { useCallback, useEffect, useRef, useState } from "react";

export function usePolling(fn, intervalMs, enabled = true) {
  // 用 ref 保存最新 fn，避免闭包捕获旧函数。
  const fnRef = useRef(fn);
  fnRef.current = fn;
  const timerRef = useRef(null);
  const [running, setRunning] = useState(enabled);

  const stop = useCallback(() => {
    if (timerRef.current !== null) {
      clearInterval(timerRef.current);
      timerRef.current = null;
    }
    setRunning(false);
  }, []);

  const start = useCallback(() => {
    stop();
    fnRef.current();
    timerRef.current = setInterval(() => fnRef.current(), intervalMs);
    setRunning(true);
  }, [intervalMs, stop]);

  useEffect(() => {
    if (enabled) start();
    else stop();
    return () => {
      if (timerRef.current !== null) {
        clearInterval(timerRef.current);
        timerRef.current = null;
      }
    };
  }, [enabled, start, stop]);

  return { start, stop, running };
}
