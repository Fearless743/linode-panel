import { useCallback, useEffect, useState } from "react";
import {
  apiFetch,
  deleteAccountGroup,
  listAccountGroups,
  listFailovers,
  listInstances,
} from "../api";
import { EmptyState, ErrorState } from "../components/ui";
import ConfirmDialog from "../components/ui/ConfirmDialog";
import { sortAccountGroups } from "../utils/accountGroups";
import AccountGroupCard from "../components/accountgroups/AccountGroupCard";
import CreateAccountGroupModal from "../components/accountgroups/CreateAccountGroupModal";
import RenameAccountGroupModal from "../components/accountgroups/RenameAccountGroupModal";
import ManageMembersModal from "../components/accountgroups/ManageMembersModal";
import FailoverConfigModal from "../components/accountgroups/FailoverConfigModal";
import InstanceConfigModal from "../components/accountgroups/InstanceConfigModal";

function errMsg(err, fallback) {
  return err && err.message ? err.message : fallback;
}

// 账号组：把多个账号（PAT）绑定成组；组内某账号的实例被封禁/余额不足时，
// 用相同配置在组内另一账号重建替换。本页负责数据编排与弹窗状态，
// 卡片与弹窗拆分到 components/accountgroups/ 下。
export default function AccountGroups() {
  const [groups, setGroups] = useState(null); // null = 加载中
  const [tokens, setTokens] = useState([]); // 本人全部账号
  const [failovers, setFailovers] = useState([]); // 按实例的替换配置与状态
  const [instances, setInstances] = useState([]); // 本人实例（用于按账号配置替换）
  const [loadError, setLoadError] = useState(""); // 账号组加载失败（整页错误态）
  const [auxErrors, setAuxErrors] = useState([]); // 辅助数据加载失败
  const [actionError, setActionError] = useState(""); // 操作类错误（内联展示）
  const [notice, setNotice] = useState("");
  const [deleting, setDeleting] = useState(false);

  // 弹窗状态。
  const [createOpen, setCreateOpen] = useState(false);
  const [renameGroup, setRenameGroup] = useState(null);
  const [manageGroup, setManageGroup] = useState(null);
  const [failoverTarget, setFailoverTarget] = useState(null); // { group, member }
  const [delGroup, setDelGroup] = useState(null);
  const [instanceConfigGroup, setInstanceConfigGroup] = useState(null);

  const load = useCallback(async () => {
    setLoadError("");
    const results = await Promise.allSettled([
      listAccountGroups(),
      apiFetch("/api/account/tokens"),
      listFailovers(),
      listInstances(),
    ]);
    const [groupsRes, tokensRes, failoversRes, instancesRes] = results;

    if (groupsRes.status === "fulfilled") {
      setGroups(Array.isArray(groupsRes.value.groups) ? groupsRes.value.groups : []);
    } else {
      setGroups([]);
      setLoadError(errMsg(groupsRes.reason, "加载账号组失败"));
    }

    // 辅助数据失败不阻塞主体展示，但错误必须可见。
    const aux = [];
    if (tokensRes.status === "fulfilled") {
      setTokens(Array.isArray(tokensRes.value.tokens) ? tokensRes.value.tokens : []);
    } else {
      aux.push("账号列表：" + errMsg(tokensRes.reason, "加载失败"));
    }
    if (failoversRes.status === "fulfilled") {
      setFailovers(
        Array.isArray(failoversRes.value.failovers) ? failoversRes.value.failovers : []
      );
    } else {
      aux.push("替换状态：" + errMsg(failoversRes.reason, "加载失败"));
    }
    if (instancesRes.status === "fulfilled") {
      setInstances(
        Array.isArray(instancesRes.value.instances) ? instancesRes.value.instances : []
      );
    } else {
      aux.push("实例列表：" + errMsg(instancesRes.reason, "加载失败"));
    }
    setAuxErrors(aux);
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  // 变更成功后刷新数据并给出提示。
  const afterMutate = useCallback(
    async (msg) => {
      setActionError("");
      setNotice(msg);
      await load();
    },
    [load]
  );

  async function doDelete() {
    if (!delGroup || deleting) return;
    setDeleting(true);
    setActionError("");
    setNotice("");
    try {
      await deleteAccountGroup(delGroup.id);
      setDelGroup(null);
      await afterMutate("账号组已删除（成员账号未被删除）");
    } catch (err) {
      setDelGroup(null);
      setActionError(errMsg(err, "删除账号组失败"));
    } finally {
      setDeleting(false);
    }
  }

  const sortedGroups = sortAccountGroups(groups || []);

  return (
    <section className="panel">
      <h2 className="panel-title">账号组</h2>

      <div className="ag-content">
        <div className="agents-section-head">
          <p className="boot-script-hint ag-hint">
            把多个账号（PAT）绑定成组；组内某账号的实例被封禁或余额不足时，可用相同配置在组内
            另一账号重建替换。
          </p>
          <button
            className="btn btn-primary"
            type="button"
            onClick={() => setCreateOpen(true)}
          >
            新建账号组
          </button>
        </div>

        {loadError && <ErrorState message={loadError} onRetry={load} />}

        {!loadError && groups === null && (
          <div className="page-status">正在加载账号组…</div>
        )}

        {!loadError && auxErrors.length > 0 && (
          <div className="users-error">部分数据加载失败：{auxErrors.join("；")}</div>
        )}

        {actionError && <div className="users-error">{actionError}</div>}
        {notice && <div className="boot-script-status ok ag-notice">{notice}</div>}

        {!loadError && groups !== null && sortedGroups.length === 0 && (
          <EmptyState text="暂无账号组，点击「新建账号组」开始。" />
        )}

        {!loadError && sortedGroups.length > 0 && (
          <div className="ag-grid">
            {sortedGroups.map((g) => (
              <AccountGroupCard
                key={g.id}
                group={g}
                tokens={tokens}
                failovers={failovers}
                onManage={setManageGroup}
                onRename={setRenameGroup}
                onDelete={setDelGroup}
                onConfigureFailover={(group, member) =>
                  setFailoverTarget({ group, member })
                }
                onOpenInstanceConfig={setInstanceConfigGroup}
              />
            ))}
          </div>
        )}
      </div>

      {createOpen && (
        <CreateAccountGroupModal
          onClose={() => setCreateOpen(false)}
          onCreated={() => afterMutate("账号组已创建")}
        />
      )}

      {renameGroup && (
        <RenameAccountGroupModal
          group={renameGroup}
          onClose={() => setRenameGroup(null)}
          onRenamed={() => afterMutate("账号组已重命名")}
        />
      )}

      {manageGroup && (
        <ManageMembersModal
          group={manageGroup}
          tokens={tokens}
          onClose={() => setManageGroup(null)}
          onChanged={() => afterMutate("成员已更新")}
        />
      )}

      {failoverTarget && (
        <FailoverConfigModal
          group={failoverTarget.group}
          member={failoverTarget.member}
          instances={instances}
          failovers={failovers}
          onClose={() => setFailoverTarget(null)}
          onChanged={() => afterMutate("替换配置已更新")}
        />
      )}

      {delGroup && (
        <ConfirmDialog
          title="删除账号组"
          message={
            "确定删除账号组「" +
            (delGroup.name || "#" + delGroup.id) +
            "」？删除后会解除组内所有账号的分组关系，但不会删除账号本身。"
          }
          confirmText="删除"
          danger
          onConfirm={doDelete}
          onCancel={() => setDelGroup(null)}
        />
      )}

      {instanceConfigGroup && (
        <InstanceConfigModal
          group={instanceConfigGroup}
          instances={instances}
          onClose={() => setInstanceConfigGroup(null)}
          onChanged={() => afterMutate("目标开机配置已更新")}
        />
      )}
    </section>
  );
}
