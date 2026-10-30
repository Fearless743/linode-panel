import { useEffect, useState } from "react";
import { listSSHKeys } from "../api";
import { useUi } from "../ui";
import { fmtDate } from "../utils";
import { Card, EmptyState, ErrorState } from "../components/ui";

export default function SSHKeys() {
  const { showBanner } = useUi();
  const [keys, setKeys] = useState(null); // null = 加载中
  const [error, setError] = useState("");
  const [reload, setReload] = useState(0);

  useEffect(() => {
    let alive = true;
    listSSHKeys()
      .then((data) => {
        if (alive) {
          setKeys(Array.isArray(data.keys) ? data.keys : []);
          setError("");
        }
      })
      .catch((err) => {
        if (alive) {
          setKeys([]);
          setError(err && err.message ? err.message : "未知错误");
          showBanner(err && err.message ? err.message : "SSH 公钥加载失败", false);
        }
      });
    return () => {
      alive = false;
    };
  }, [showBanner, reload]);

  return (
    <Card title="SSH 公钥">
      <div className="ssh-keys-content">
        {keys === null && <EmptyState text="正在加载…" />}
        {keys !== null && error && (
          <ErrorState
            message={"加载失败：" + error}
            onRetry={() => {
              setError("");
              setKeys(null);
              setReload((r) => r + 1);
            }}
          />
        )}
        {keys !== null && !error && keys.length === 0 && (
          <EmptyState text="未添加公钥" />
        )}
        {keys !== null &&
          !error &&
          keys.map((k) => (
            <div className="ssh-key-item" key={k.id || k.label}>
              <div>
                <span className="ssh-key-label">{k.label || "（未命名）"}</span>
                {k.created && (
                  <span className="ssh-key-meta">{fmtDate(k.created)}</span>
                )}
              </div>
              <div className="ssh-key-body" title={k.key || ""}>
                {k.key || ""}
              </div>
            </div>
          ))}
      </div>
    </Card>
  );
}
