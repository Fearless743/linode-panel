// 新建 Agent 后的一次性凭据模态框：agent_id / agent_token / registration_code
// + 一键安装命令 + 复制按钮。
import Modal from "../Modal.jsx";
import Button from "../ui/Button.jsx";
import { copyText, agentInstallCommand } from "../../utils";
import { useUi } from "../../ui";

export default function AgentCreatedModal({ res, onClose }) {
  const { showBanner } = useUi();
  // 一键安装命令：面板地址 + 预签发 token + 注册码，远程主机粘贴即用。
  const installCommand = agentInstallCommand({
    origin: typeof window === "undefined" ? "" : window.location.origin,
    token: res && res.agent_token ? res.agent_token : "",
    code: res && res.registration_code ? res.registration_code : "",
  });

  return (
    <Modal title="新 Agent 已创建" onClose={onClose}>
      <p className="secret-warning">以下凭据仅此一次可见，请立即保存！</p>
      {res && res.agent_id != null && (
        <div className="secret-row">
          <span className="agents-muted">Agent ID：</span>
          <span>{String(res.agent_id)}</span>
        </div>
      )}
      <div className="secret-row">
        <span className="agents-muted">Agent Token：</span>
        <span className="secret-box">{res && res.agent_token ? res.agent_token : "（无）"}</span>
        <Button
          variant="ghost"
          className="modal-copy-btn"
          onClick={() => copyText((res && res.agent_token) || "", () => {})}
        >
          复制
        </Button>
      </div>
      <div className="secret-row">
        <span className="agents-muted">注册码：</span>
        <span className="secret-box">
          {res && res.registration_code ? res.registration_code : "（无）"}
        </span>
        <Button
          variant="ghost"
          className="modal-copy-btn"
          onClick={() => copyText((res && res.registration_code) || "", () => {})}
        >
          复制
        </Button>
      </div>
      <div className="secret-row">
        <span className="agents-muted">一键安装命令：</span>
        <span className="secret-box">{installCommand || "（无法获取面板地址）"}</span>
        <Button
          variant="ghost"
          className="modal-copy-btn"
          disabled={!installCommand}
          onClick={() => copyText(installCommand, () => showBanner("已复制安装命令", false))}
        >
          复制
        </Button>
      </div>
      <p className="agents-muted">
        在目标主机安装 linode-panel-agent 二进制后执行该命令即可接入；命令含 token 与注册码，仅此一次可见。
      </p>
      <div className="modal-actions">
        <Button onClick={onClose}>我已保存</Button>
      </div>
    </Modal>
  );
}
