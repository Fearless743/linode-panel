import { useState } from "react";
import Modal from "../Modal";
import { addAccountGroupToken, removeAccountGroupToken } from "../../api";

// ---------- 管理账号组成员模态框 ----------
// 展示当前用户的全部账号（GET /api/account/tokens），勾选即加入、取消即移出。
// 移出只解除分组关系，不会删除账号。
export default function ManageMembersModal({ group, tokens, onClose, onChanged }) {
  const [memberIds, setMemberIds] = useState(() =>
    (Array.isArray(group.token_ids) ? group.token_ids : []).map(String)
  );
  const [busyId, setBusyId] = useState(null);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  const list = Array.isArray(tokens) ? tokens : [];
  const anyBusy = busyId !== null;

  async function toggle(t, joined) {
    setBusyId(String(t.id));
    setError("");
    setNotice("");
    try {
      if (joined) {
        await removeAccountGroupToken(group.id, t.id);
        setMemberIds((prev) => prev.filter((id) => id !== String(t.id)));
        setNotice("已移出账号：" + (t.label || "#" + t.id));
      } else {
        await addAccountGroupToken(group.id, t.id);
        setMemberIds((prev) =>
          prev.includes(String(t.id)) ? prev : [...prev, String(t.id)]
        );
        setNotice("已加入账号：" + (t.label || "#" + t.id));
      }
      if (onChanged) onChanged();
    } catch (err) {
      setError(
        err && err.message
          ? err.message
          : joined
            ? "移出账号失败"
            : "加入账号失败"
      );
    } finally {
      setBusyId(null);
    }
  }

  return (
    <Modal title={"管理成员 · " + (group.name || "#" + group.id)} onClose={onClose}>
      <p className="modal-detail">
        勾选账号即加入该组，取消勾选即移出。移出只解除分组关系，不会删除账号。
      </p>

      {list.length === 0 ? (
        <div className="users-empty">尚未绑定任何账号，请先到「账号管理」绑定账号。</div>
      ) : (
        <ul className="ag-members">
          {list.map((t) => {
            const joined = memberIds.includes(String(t.id));
            const busy = busyId === String(t.id);
            return (
              <li className="ag-member" key={t.id}>
                <label className="ag-member-check">
                  <input
                    type="checkbox"
                    checked={joined}
                    disabled={anyBusy}
                    onChange={() => toggle(t, joined)}
                  />
                  <span className="ag-member-label">{t.label || "（未命名）"}</span>
                  <span className="ag-member-id">#{t.id}</span>
                </label>
                {busy && <span className="ag-member-busy">处理中…</span>}
                {joined && !busy && <span className="perm-chip">已加入</span>}
              </li>
            );
          })}
        </ul>
      )}

      {error && <div className="modal-error">{error}</div>}
      {notice && <div className="boot-script-status ok">{notice}</div>}

      <div className="modal-actions">
        <button className="btn btn-ghost" type="button" onClick={onClose} disabled={anyBusy}>
          关闭
        </button>
      </div>
    </Modal>
  );
}
