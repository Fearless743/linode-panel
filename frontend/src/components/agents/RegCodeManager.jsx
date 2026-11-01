// 注册码管理：设置注册码（弹窗）/ 生成新码。
// onSetRegcode / onGenRegcode 由页面编排（API 调用 + banner）。
import { useState } from "react";
import Button from "../ui/Button.jsx";
import SetRegcodeModal from "./SetRegcodeModal.jsx";

export default function RegCodeManager({ onSetRegcode, onGenRegcode }) {
  const [setOpen, setSetOpen] = useState(false);

  return (
    <div className="agents-section">
      <div className="agents-section-head">
        <h3 className="agents-section-title">注册码管理</h3>
        <div className="regcode-actions">
          <Button variant="ghost" onClick={() => setSetOpen(true)}>
            设置注册码
          </Button>
          <Button variant="ghost" onClick={onGenRegcode}>
            生成新码
          </Button>
        </div>
      </div>
      <p className="agents-muted">
        注册码是 Agent 接入面板的唯一凭据：远程主机首次运行 agent 时用 --code 注册即可。
      </p>
      {setOpen && (
        <SetRegcodeModal
          onSetRegcode={onSetRegcode}
          onClose={() => setSetOpen(false)}
        />
      )}
    </div>
  );
}
