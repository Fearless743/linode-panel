// 账号健康状态单元格：复用 status-pill 样式，按 health_status 着色。
// ok=绿（status-running）、no_balance=黄（status-transition）、
// banned=红（status-danger）、error/未探测=灰（status-unknown）。
import { HEALTH_STATUS_LABELS } from "../../constants";

const PILL_CLS = {
  ok: "status-running",
  no_balance: "status-transition",
  banned: "status-danger",
};

// 余额显示：欠款 + 未出账（正数=欠款）；探测失败或未返回时显示 "—"。
function fmtBalance(token) {
  if (token.balance == null && token.balance_uninvoiced == null) return "—";
  const due = (token.balance ?? 0).toFixed(2);
  const unbilled = (token.balance_uninvoiced ?? 0).toFixed(2);
  return "欠款 " + due + " / 未出账 " + unbilled;
}

export default function AccountHealthCell({ token }) {
  const status = token.health_status || "";
  const cls = "status-pill " + (PILL_CLS[status] || "status-unknown");
  const label = status ? HEALTH_STATUS_LABELS[status] || status : "未检测";
  const tip =
    token.health_detail ||
    (token.health_checked_at
      ? "检测时间 " + token.health_checked_at
      : "尚未检测");
  return (
    <>
      <span className={cls} title={tip}>
        <span>{label}</span>
      </span>
      <div className="acct-balance" title="余额（欠款 / 未出账）">
        {fmtBalance(token)}
      </div>
    </>
  );
}
