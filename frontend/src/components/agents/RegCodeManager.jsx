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
        注册码用于派生 AES-256-GCM 密钥，加密下发给 Agent 的 PAT 只能被持有该码的 Agent 解密。
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
