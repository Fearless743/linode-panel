// TG 通知设置（管理员）：全局 Bot Token + Chat ID，被墙轮换/重建成功后通知新 IP。
import { useCallback, useEffect, useState } from "react";
import { getNotify, setNotify, testNotify } from "../api";

export default function Notify() {
  const [enabled, setEnabled] = useState(false);
  const [chatId, setChatId] = useState("");
  const [botToken, setBotToken] = useState("");
  const [hasToken, setHasToken] = useState(false);
  const [loading, setLoading] = useState(true);
  const [status, setStatus] = useState(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const data = await getNotify();
      setEnabled(!!data.enabled);
      setChatId(data.chat_id || "");
      setHasToken(!!data.has_token);
      setStatus(null);
    } catch (err) {
      setStatus({ msg: "加载失败：" + (err.message || "未知错误"), cls: "error" });
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  async function save() {
    setBusy(true);
    setStatus({ msg: "保存中…", cls: "" });
    try {
      const data = await setNotify({ enabled, chat_id: chatId.trim(), bot_token: botToken });
      setHasToken(!!data.has_token);
      setBotToken("");
      setStatus({ msg: "已保存", cls: "ok" });
    } catch (err) {
      setStatus({ msg: "保存失败：" + (err.message || "未知错误"), cls: "error" });
    } finally {
      setBusy(false);
    }
  }

  async function sendTest() {
    setBusy(true);
    setStatus({ msg: "发送中…", cls: "" });
    try {
      await testNotify();
      setStatus({ msg: "测试消息已发送，请查看 TG", cls: "ok" });
    } catch (err) {
      setStatus({ msg: "发送失败：" + (err.message || "未知错误"), cls: "error" });
    } finally {
      setBusy(false);
    }
  }

  if (loading) return <div className="page-status">正在加载…</div>;

  return (
    <section className="panel">
      <h2 className="panel-title">通知设置</h2>
      <p className="boot-script-hint">
        配置 Telegram Bot 后，被墙触发双机轮换 / 自动换 IP 重建成功时会通知旧 IP → 新 IP，并显示距离上次被墙的间隔。
        获取方式：与 @BotFather 创建 Bot 拿到 Token，与 @userinfobot 获取 Chat ID。
      </p>
      <div className="modal-field">
        <label className="exec-option">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
            disabled={busy}
          />
          <span className="exec-option-text">
            <span>开启 TG 通知</span>
            <span className="exec-option-sub">关闭后被墙不再推送</span>
          </span>
        </label>
      </div>
      <div className="modal-field">
        <label htmlFor="notify-chat">Chat ID</label>
        <input
          id="notify-chat"
          className="modal-input"
          type="text"
          placeholder="例如 123456789"
          value={chatId}
          onChange={(e) => setChatId(e.target.value)}
          disabled={busy}
        />
      </div>
      <div className="modal-field">
        <label htmlFor="notify-token">Bot Token{hasToken ? "（已配置，留空不改）" : "（必填）"}</label>
        <input
          id="notify-token"
          className="modal-input"
          type="password"
          autoComplete="new-password"
          placeholder={hasToken ? "留空保持不变" : "例如 123456:ABC-DEF..."}
          value={botToken}
          onChange={(e) => setBotToken(e.target.value)}
          disabled={busy}
        />
      </div>
      {status && <div className={"boot-script-status " + status.cls}>{status.msg}</div>}
      <div className="modal-actions">
        <button className="btn btn-ghost" type="button" onClick={load} disabled={busy}>
          重新加载
        </button>
        <button className="btn btn-ghost" type="button" onClick={sendTest} disabled={busy}>
          发送测试
        </button>
        <button className="btn btn-primary" type="button" onClick={save} disabled={busy}>
          {busy ? "保存中…" : "保存"}
        </button>
      </div>
    </section>
  );
}
