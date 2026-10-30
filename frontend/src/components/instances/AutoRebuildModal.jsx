// 「被墙后自动重建换 IP」配置弹窗：为单个实例开启/关闭检测，并展示最近一次
// 检测结果与重建状态。启用后由面板后台监测：国内探测连续失败达到阈值即自动
// 删机重建，使实例换到新的公网 IP。
import { useEffect, useState } from "react";
import {
  checkAutoRebuild,
  deleteAutoRebuild,
  getAutoRebuild,
  setAutoRebuild,
} from "../../api";
import Modal from "../Modal";
import { Button, FormField } from "../ui";

const STATUS_LABELS = {
  ok: "连通",
  blocked: "疑似被墙",
  rebuilding: "重建换 IP 中",
  error: "检测出错",
  idle: "未检测（实例未运行）",
};

function fmtTime(s) {
  if (!s) return "—";
  const d = new Date(s);
  if (isNaN(d.getTime())) return s;
  return d.toLocaleString();
}

export default function AutoRebuildModal({ inst, onClose, onChanged }) {
  const [enabled, setEnabled] = useState(false);
  const [port, setPort] = useState("22");
  const [threshold, setThreshold] = useState("3");
  const [intervalMin, setIntervalMin] = useState("5");
  const [state, setState] = useState(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState(null); // { msg, cls }
  const [error, setError] = useState("");

  useEffect(() => {
    let alive = true;
    getAutoRebuild(inst.id)
      .then((data) => {
        if (!alive) return;
        setState(data);
        setEnabled(!!data.enabled);
        setPort(String(data.port || 22));
        setThreshold(String(data.threshold || 3));
        setIntervalMin(String(Math.round((data.interval_seconds || 300) / 60) || 5));
        setLoading(false);
      })
      .catch((err) => {
        if (!alive) return;
        setError((err && err.message) || "加载失败");
        setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [inst.id]);

  const tokenId = inst && inst.token_id != null ? inst.token_id : undefined;

  async function save() {
    const p = parseInt(port, 10);
    const t = parseInt(threshold, 10);
    const m = parseInt(intervalMin, 10);
    if (!(p >= 1 && p <= 65535)) {
      setStatus({ msg: "端口必须在 1-65535 之间", cls: "error" });
      return;
    }
    if (!(t >= 1 && t <= 20)) {
      setStatus({ msg: "连续失败次数必须在 1-20 之间", cls: "error" });
      return;
    }
    if (!(m >= 1 && m <= 1440)) {
      setStatus({ msg: "检测间隔必须在 1-1440 分钟之间", cls: "error" });
      return;
    }
    setBusy(true);
    setStatus({ msg: "保存中…", cls: "" });
    try {
      const data = await setAutoRebuild(inst.id, {
        enabled,
        port: p,
        threshold: t,
        interval_seconds: m * 60,
        token_id: tokenId,
      });
      setState(data);
      setStatus({ msg: enabled ? "已开启自动换 IP" : "已保存（未开启）", cls: "ok" });
      if (onChanged) onChanged();
    } catch (err) {
      setStatus({ msg: "保存失败：" + ((err && err.message) || "未知错误"), cls: "error" });
    } finally {
      setBusy(false);
    }
  }

  async function disable() {
    setBusy(true);
    setStatus({ msg: "处理中…", cls: "" });
    try {
      await deleteAutoRebuild(inst.id);
      setEnabled(false);
      setState(null);
      setStatus({ msg: "已关闭并移除配置", cls: "ok" });
      if (onChanged) onChanged();
    } catch (err) {
      setStatus({ msg: "关闭失败：" + ((err && err.message) || "未知错误"), cls: "error" });
    } finally {
      setBusy(false);
    }
  }

  async function checkNow() {
    setBusy(true);
    setStatus({ msg: "检测中…", cls: "" });
    try {
      const data = await checkAutoRebuild(inst.id);
      setState(data);
      const label = STATUS_LABELS[data.last_status] || data.last_status || "";
      setStatus({ msg: "检测完成：" + label, cls: "ok" });
      if (onChanged) onChanged();
    } catch (err) {
      setStatus({ msg: "检测失败：" + ((err && err.message) || "未知错误"), cls: "error" });
    } finally {
      setBusy(false);
    }
  }

  const configured = state != null;

  return (
    <Modal title={"自动换 IP · " + (inst.label || "#" + inst.id)} onClose={onClose}>
      {loading && <div className="boot-script-status">正在加载…</div>}
      {error && <div className="boot-script-status error">{error}</div>}
      {!loading && !error && (
        <>
          <p className="boot-script-hint">
            开启后，面板会定期从国内探测该实例的端口；连续失败达到阈值即自动「删机重建」，
            实例换到新的公网 IP。<strong>重建会删除旧实例及数据</strong>，请确保已备份。
          </p>
          <FormField label="开启自动换 IP">
            <label className="exec-option">
              <input
                type="checkbox"
                checked={enabled}
                onChange={(e) => setEnabled(e.target.checked)}
                disabled={busy}
              />
              <span className="exec-option-text">
                <span>被墙后自动删机重建换 IP</span>
                <span className="exec-option-sub">使用该实例配置的开机脚本；未配置则用内置默认脚本</span>
              </span>
            </label>
          </FormField>
          <FormField label="检测端口（TCP）" htmlFor="ar-port">
            <input
              id="ar-port"
              className="modal-input"
              type="number"
              min="1"
              max="65535"
              value={port}
              onChange={(e) => setPort(e.target.value)}
              disabled={busy}
            />
          </FormField>
          <FormField label="连续失败多少次判定被墙" htmlFor="ar-threshold">
            <input
              id="ar-threshold"
              className="modal-input"
              type="number"
              min="1"
              max="20"
              value={threshold}
              onChange={(e) => setThreshold(e.target.value)}
              disabled={busy}
            />
          </FormField>
          <FormField label="检测间隔（分钟）" htmlFor="ar-interval">
            <input
              id="ar-interval"
              className="modal-input"
              type="number"
              min="1"
              max="1440"
              value={intervalMin}
              onChange={(e) => setIntervalMin(e.target.value)}
              disabled={busy}
            />
          </FormField>

          {configured && (
            <div className="modal-detail auto-rebuild-state">
              <div>当前状态：{STATUS_LABELS[state.last_status] || state.last_status || "—"}</div>
              <div>连续失败：{state.fail_count || 0}</div>
              <div>最近检测：{fmtTime(state.last_check_at)}</div>
              <div>最近被墙：{fmtTime(state.last_blocked_at)}</div>
              {state.last_task_id ? <div>最近任务：{state.last_task_id}</div> : null}
              {state.last_detail ? <div>详情：{state.last_detail}</div> : null}
            </div>
          )}

          <div className="modal-actions">
            <span className={"boot-script-status" + (status ? " " + status.cls : "")}>
              {status ? status.msg : ""}
            </span>
            {configured && (
              <Button variant="ghost" onClick={checkNow} disabled={busy}>
                立即检测
              </Button>
            )}
            {configured && (
              <Button variant="danger" onClick={disable} disabled={busy}>
                关闭
              </Button>
            )}
            <Button variant="primary" onClick={save} disabled={busy}>
              保存
            </Button>
            <Button variant="ghost" onClick={onClose} disabled={busy}>
              取消
            </Button>
          </div>
        </>
      )}
    </Modal>
  );
}
