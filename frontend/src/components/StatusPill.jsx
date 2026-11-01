// 状态 pill（复用现有 style.css 的 status-pill 系列）。
// 中文标签统一取自 constants.js 的 STATUS_LABELS，不再本地硬编码。
import { STATUS_LABELS } from "../constants";

// 过渡态（黄色 + spinner）。保留本地 3 元素子集（booting/rebooting/shutting_down），
// 与原先视觉/行为完全等价；不直接复用 constants.TRANSITION_STATES（它含更多状态，
// 如 provisioning/rebuilding 等，会改变渲染结果）。running 绿、offline 灰、未知 default。
const YELLOW_STATES = new Set(["booting", "rebooting", "shutting_down"]);

export default function StatusPill({ status }) {
  let cls = "status-pill status-unknown";
  if (status === "running") cls = "status-pill status-running";
  else if (status === "offline") cls = "status-pill status-offline";
  else if (status === "unavailable") cls = "status-pill status-unavailable";
  else if (YELLOW_STATES.has(status)) cls = "status-pill status-transition";
  return (
    <span className={cls}>
      {YELLOW_STATES.has(status) && <span className="spinner" />}
      <span>{STATUS_LABELS[status] || status}</span>
    </span>
  );
}
