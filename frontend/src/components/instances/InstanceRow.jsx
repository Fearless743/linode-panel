// 实例行：名称/标签、状态 pill、地区/型号、公网 IP、操作按钮，
// 以及运行中实例的 SSH 卡片与失败提示行。
import { Fragment } from "react";
import StatusPill from "../StatusPill";
import SshCard from "./SshCard";
import { isTransition } from "./useInstancePolling";
import { regionLabel, typeLabel } from "../../utils/linodeLabels";

export default function InstanceRow({
  inst,
  st,
  rebuildRunning,
  hasCustomScript,
  autoRebuildEnabled,
  hasPerm,
  onAction,
  onRebuild,
  onEdit,
  onMigrate,
  onDelete,
}) {
  const missing = inst.missing === true;
  const status = st.lastStatus || inst.status;
  const tags = Array.isArray(inst.tags) ? inst.tags : [];
  const ipv4 = Array.isArray(inst.ipv4) ? inst.ipv4 : [];
  // 运行中且已有探测结果（就绪/超时/出错）时展示 SSH 卡片。
  const showSSH =
    !missing &&
    status === "running" &&
    st.statusData &&
    (st.sshReady || st.sshProbeDone || st.sshError);

  const renderActions = () => {
    const busy = isTransition(status);
    const canPower = hasPerm("power");
    // 刷新按钮已上移到账号分组行（按账号批量刷新），行内不再提供。
    const buttons = [];
    if (missing) {
      // 上游已不存在：不提供开关机/重建，只允许迁移到其他账号或删除记录。
      if (canPower) {
        buttons.push(
          <button
            key="migrate"
            className="action-btn primary"
            type="button"
            title="用缓存配置在另一个账号上重建该实例并迁移面板配置"
            onClick={() => onMigrate(inst)}
          >
            迁移
          </button>
        );
      }
    } else {
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
            disabled={busy || rebuildRunning}
            title={rebuildRunning ? "该实例已有重建任务进行中" : ""}
            onClick={() => onRebuild(inst)}
          >
            重建
          </button>
        );
      }
    }
    // 「编辑」按钮：合并开机脚本与自动换 IP 两个编辑区；需要其中任一权限才显示。
    if (hasPerm("script") || hasPerm("rebuild")) {
      const marks = [hasCustomScript ? "脚本" : "", autoRebuildEnabled ? "自动换IP" : ""]
        .filter(Boolean)
        .join("、");
      buttons.push(
        <button
          key="edit"
          className="action-btn"
          type="button"
          title={
            "编辑：" +
            [hasPerm("script") ? "开机脚本" : "", hasPerm("rebuild") ? "自动换 IP" : ""]
              .filter(Boolean)
              .join("、") +
            (marks ? "（已配置：" + marks + "）" : "")
          }
          onClick={() => onEdit(inst)}
        >
          编辑
          {(hasCustomScript || autoRebuildEnabled) && " •"}
        </button>
      );
    }
    // 「删除」按钮：需 power 权限；过渡态（操作进行中）禁用，避免误删。
    // 对不可用实例，删除只会清理面板中的缓存记录及其配置。
    if (canPower) {
      buttons.push(
        <button
          key="delete"
          className="action-btn danger"
          type="button"
          disabled={busy}
          onClick={() => onDelete(inst)}
        >
          {missing ? "删除记录" : "删除"}
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
          <div>{inst.region ? regionLabel(inst.region) : "—"}</div>
          <div className="ip-muted">{inst.type ? typeLabel(inst.type) : ""}</div>
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
