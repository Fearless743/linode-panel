// 全局 UI 状态：顶部 banner（错误 / 提示 / 重试）。
import { createContext, useCallback, useContext, useMemo, useState } from "react";

const UiContext = createContext(null);

export function UiProvider({ children }) {
  // banner = { msg, withRetry } | null
  const [banner, setBanner] = useState(null);

  const showBanner = useCallback((msg, withRetry = false) => {
    setBanner({ msg: String(msg), withRetry: !!withRetry });
  }, []);
  const hideBanner = useCallback(() => setBanner(null), []);

  const value = useMemo(
    () => ({ banner, showBanner, hideBanner }),
    [banner, showBanner, hideBanner]
  );
  return <UiContext.Provider value={value}>{children}</UiContext.Provider>;
}

export function useUi() {
  return useContext(UiContext);
}
