// SSH 命令卡：展示实例 SSH 连接命令 + 端口探测状态行。
// 端口探测状态由 useInstancePolling 写入行状态（sshReady/sshTimeout/sshError）。

function makeProbeLine(st) {
  if (st.sshReady) {
    return <div className="ssh-probe ok">✓ SSH 端口可达</div>;
  }
  if (st.sshTimeout) {
    return <div className="ssh-probe error">SSH 仍不可达，可稍后手动刷新</div>;
  }
  if (st.sshError) {
    return <div className="ssh-probe error">SSH 端口未就绪：{st.sshError}</div>;
  }
  return (
    <div className="ssh-probe">
      <span className="spinner" />
      检测中…
    </div>
  );
}

export default function SshCard({ st }) {
  return (
    <tr className="ssh-row">
      <td colSpan={5}>
        <div className="ssh-card">
          <div className="ssh-card-row">
            <span className="ssh-command">{st.statusData.ssh_command || ""}</span>
          </div>
          {makeProbeLine(st)}
        </div>
      </td>
    </tr>
  );
}
