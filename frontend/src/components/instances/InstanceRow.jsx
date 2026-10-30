// 实例行：名称/标签、状态 pill、地区/型号、公网 IP、操作按钮，
// 以及运行中实例的 SSH 卡片与失败提示行。
import { Fragment } from "react";
import StatusPill from "../StatusPill";
import SshCard from "./SshCard";
import { isTransition } from "./useInstancePolling";

export default function InstanceRow({
  inst,
  st,
  rebuildTaskRunning,
  hasCustomScript,
  autoRebuildEnabled,
  hasPerm,
  onAction,
  onRebuild,
  onScript,
  onAutoRebuild,
  onRefresh,
}) {
  const status = st.lastStatus || inst.status;
  const tags = Array.isArray(inst.tags) ? inst.tags : [];
  const ipv4 = Array.isArray(inst.ipv4) ? inst.ipv4 : [];
  // 运行中且已有探测结果（就绪/超时/出错）时展示 SSH 卡片。
  const showSSH =
    status === "running" && st.statusData && (st.sshReady || st.sshProbeDone || st.sshError);

  const renderActions = () => {
    const busy = isTransition(status);
    const canPower = hasPerm("power");
    // 行级刷新：始终可用，通过 API 获取该实例最新数据。
    const buttons = [
      <button
        key="refresh"
        className="action-btn"
        type="button"
        title="通过 API 刷新该实例"
        onClick={() => onRefresh(inst)}
      >
        刷新
      </button>,
    ];
    if (canPower && status === "offline") {
      buttons.push(
        <button
          key="boot"
          className="action-btn primary"
          type="button"
          onClick={() => onAction(inst, "boot")}
        >
          开机
        </button>
      );
    } else if (canPower && status === "running") {
      buttons.push(
        <button
          key="shutdown"
          className="action-btn"
          type="button"
          onClick={() => onAction(inst, "shutdown")}
        >
          关机
        </button>,
        <button
          key="reboot"
          className="action-btn danger"
          type="button"
          onClick={() => onAction(inst, "reboot")}
        >
          重启
        </button>
      );
    } else if (busy) {
      buttons.push(
        <button key="busy" className="action-btn" type="button" disabled>
          处理中…
        </button>
      );
    }
    if (hasPerm("rebuild")) {
      buttons.push(
        <button
          key="rebuild"
          className="action-btn danger"
          type="button"
          disabled={busy || rebuildTaskRunning}
          title={rebuildTaskRunning ? "已有重建任务进行中" : ""}
          onClick={() => onRebuild(inst)}
        >
          重建
        </button>
      );
    }
    if (hasPerm("script")) {
      buttons.push(
        <button
          key="script"
          className="action-btn"
          type="button"
          title={hasCustomScript ? "已配置自定义开机脚本" : "配置开机脚本"}
          onClick={() => onScript(inst)}
        >
          {hasCustomScript ? "脚本 •" : "脚本"}
        </button>
      );
    }
    if (hasPerm("rebuild")) {
      buttons.push(
        <button
          key="auto-rebuild"
          className={autoRebuildEnabled ? "action-btn primary" : "action-btn"}
          type="button"
          title={
            autoRebuildEnabled
              ? "已开启：被墙后自动删机重建换 IP"
              : "被墙后自动删机重建换 IP"
          }
          onClick={() => onAutoRebuild(inst)}
        >
          {autoRebuildEnabled ? "自动换IP •" : "自动换IP"}
        </button>
      );
    }
    return <div className="actions">{buttons}</div>;
  };

  return (
    <Fragment>
      <tr>
        <td>
          <div className="instance-name">
            {inst.label || "（未命名）"}
            <span className="instance-id">#{inst.id}</span>
          </div>
          {tags.length > 0 && (
            <div className="tags">
              {tags.map((t) => (
                <span className="tag" key={t}>
                  {t}
                </span>
              ))}
            </div>
          )}
        </td>
        <td>
          <StatusPill status={status} />
        </td>
        <td>
          <div>{inst.region || "—"}</div>
          <div className="ip-muted">{inst.type || ""}</div>
        </td>
        <td>
          <div className="ip-cell">
            {ipv4.length > 0
              ? ipv4.map((ip) => (
                  <span className="ip-row" key={ip}>
                    {ip}
                  </span>
                ))
              : (
                <span className="ip-row ip-muted">—</span>
              )}
            {inst.ipv6 && <span className="ip-row ip-muted">{inst.ipv6}</span>}
          </div>
        </td>
        <td>{renderActions()}</td>
      </tr>
      {showSSH && <SshCard st={st} />}
      {st.failMsg && (
        <tr className="fail-row">
          <td colSpan={5}>
            <div className="ssh-card">
              <div className="ssh-probe error">{st.failMsg}</div>
            </div>
          </td>
        </tr>
      )}
      {inst.fetch_error && (
        <tr className="fail-row">
          <td colSpan={5}>
            <div className="ssh-card">
              <div className="ssh-probe warn">{inst.fetch_error}</div>
            </div>
          </td>
        </tr>
      )}
    </Fragment>
  );
}
