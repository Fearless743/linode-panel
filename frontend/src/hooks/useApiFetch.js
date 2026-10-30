// 通用数据请求 Hook：封装 fetch 生命周期（loading / error / data / reload），
// 组件卸载后不再 setState。

import { useCallback, useEffect, useRef, useState } from "react";
import { apiFetch } from "../api";

export function useApiFetch(url, options = {}) {
  const [data, setData] = useState(null);
  const [error, setError] = useState(null);
  const [loading, setLoading] = useState(false);

  // 组件是否仍然挂载（卸载后禁止 setState）。
  const mountedRef = useRef(true);
  // 用 ref 保存 options，避免内联对象导致 reload 引用不稳定而重复请求。
  const optionsRef = useRef(options);
  optionsRef.current = options;
  // 请求序号，仅采纳最后一次发起的请求结果。
  const requestIdRef = useRef(0);

  const reload = useCallback(() => {
    const id = ++requestIdRef.current;
    setLoading(true);
    setError(null);
    apiFetch(url, optionsRef.current)
      .then((body) => {
        if (mountedRef.current && id === requestIdRef.current) {
          setData(body);
          setError(null);
        }
      })
      .catch((err) => {
        if (mountedRef.current && id === requestIdRef.current) {
          setError(err);
        }
      })
      .finally(() => {
        if (mountedRef.current && id === requestIdRef.current) {
          setLoading(false);
        }
      });
  }, [url]);

  useEffect(() => {
    mountedRef.current = true;
    reload();
    return () => {
      mountedRef.current = false;
    };
  }, [reload]);

  return { data, error, loading, reload };
}
