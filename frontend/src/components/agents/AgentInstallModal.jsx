// 生成新注册码后的模态框：展示注册码 + 一键安装命令。
import Modal from "../Modal.jsx";
import Button from "../ui/Button.jsx";
import { copyText, agentInstallCommand } from "../../utils";
import { useUi } from "../../ui";

export default function AgentInstallModal({ code, onClose }) {
  const { showBanner } = useUi();
  const installCommand = agentInstallCommand({
    origin: typeof window === "undefined" ? "" : window.location.origin,
    code,
  });

  return (
    <Modal title="注册码已生成" onClose={onClose}>
      <p className="secret-warning">注册码是 Agent 接入的唯一凭据，请妥善保存。</p>
      <div className="secret-row">
        <span className="agents-muted">注册码：</span>
        <span className="secret-box">{code || "（无）"}</span>
        <Button
          variant="ghost"
          className="modal-copy-btn"
          onClick={() => copyText(code || "", () => {})}
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
        在目标主机安装 linode-panel-agent 二进制后执行该命令即可接入。
      </p>
      <div className="modal-actions">
        <Button onClick={onClose}>我已保存</Button>
      </div>
    </Modal>
  );
}
