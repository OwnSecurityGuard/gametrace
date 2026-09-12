import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import {
  Users,
  Trash2,
  KeyRound,
  ShieldCheck,
} from "lucide-react";
import {
  useListUsers,
  useRevokeUser,
} from "@/hooks/use-mcp";
import { toast } from "@/components/ui/toast";
import { getIdentity } from "@/lib/auth";
import type { GtaUser } from "@/types/access-code";

function formatTime(ts: string): string {
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleString();
}

/**
 * 「成员管理」对话框：自助注册成员账号列表与撤销。
 *
 *  - 自助注册（主路径）：对方在「设置 → 快速开始」起个用户名即可，无需任何操作。
 *
 * 成员账号列表区分来源：env bootstrap（本机配置，不可撤销）/ 自助注册。
 */
export function MembersAdminDialog({
  open,
  onClose,
}: {
  open: boolean;
  onClose: () => void;
}) {
  return (
    <Dialog
      open={open}
      onClose={onClose}
      icon={<Users className="h-5 w-5" />}
      title="成员管理"
      footer={
        <Button variant="outline" onClick={onClose}>
          关闭
        </Button>
      }
    >
      <div className="space-y-5">
        <AccountsSection />
      </div>
    </Dialog>
  );
}

/** 成员账号列表：bootstrap（只读）+ users 表（自助注册），admin 可撤销。 */
function AccountsSection() {
  const { data, error, isLoading } = useListUsers();
  const revokeUser = useRevokeUser();
  const [busy, setBusy] = useState<string | null>(null);

  // 非 admin：list_users 返回 ok=false → callTool 抛错 → query error。静默隐藏。
  if (error) return null;
  const users = data?.users ?? [];
  const bootstrap = data?.bootstrap_owners ?? [];
  const self = getIdentity()?.owner ?? "";

  async function handleRevoke(u: GtaUser) {
    if (!window.confirm(`撤销用户 ${u.owner}？其 token 将立即失效，且无法恢复。`)) return;
    setBusy(u.owner);
    try {
      await revokeUser.mutateAsync({ owner: u.owner });
      toast.success("已撤销", `${u.owner} 的 token 已失效`);
    } catch (e) {
      toast.error("撤销失败", e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(null);
    }
  }

  return (
    <section>
      <label className="flex items-center gap-1.5 text-sm font-medium">
        <KeyRound className="h-3.5 w-3.5 text-muted-foreground" />
        成员账号（{users.length + bootstrap.length}）
      </label>
      {isLoading ? (
        <p className="mt-2 text-sm text-muted-foreground">加载中…</p>
      ) : (
        <div className="mt-2 divide-y rounded-md border border-border">
          {bootstrap.map((owner) => (
            <div key={owner} className="flex items-center gap-2 px-3 py-2 text-sm">
              <span className="font-mono">{owner}</span>
              <span className="rounded bg-primary/10 px-1.5 py-0.5 text-[11px] text-primary">admin</span>
              <span className="rounded bg-muted px-1.5 py-0.5 text-[11px] text-muted-foreground">
                本机配置（GT_AUTH_TOKENS）
              </span>
              <span className="ml-auto text-[11px] text-muted-foreground">不可撤销</span>
            </div>
          ))}
          {users.map((u) => (
            <div key={u.owner} className="flex items-center gap-2 px-3 py-2 text-sm">
              <span className="font-mono">{u.owner}</span>
              {u.is_admin && (
                <span className="flex items-center gap-0.5 rounded bg-primary/10 px-1.5 py-0.5 text-[11px] text-primary">
                  <ShieldCheck className="h-3 w-3" />
                  admin
                </span>
              )}
              <span className="rounded bg-muted px-1.5 py-0.5 text-[11px] text-muted-foreground">
                {u.created_by ? `由 ${u.created_by} 添加` : "自助注册"}
              </span>
              <span className="ml-auto text-[11px] text-muted-foreground">{formatTime(u.created_at)}</span>
              {u.owner !== self && (
                <Button
                  variant="outline"
                  size="sm"
                  disabled={busy === u.owner}
                  onClick={() => handleRevoke(u)}
                >
                  <Trash2 className="h-3.5 w-3.5" />
                  {busy === u.owner ? "撤销中…" : "撤销"}
                </Button>
              )}
            </div>
          ))}
        </div>
      )}
      <p className="mt-1.5 text-xs text-muted-foreground">
        撤销仅使其 token 立即失效；该用户名随即释放，可被重新注册（项目归属按用户名自动恢复）。
      </p>
    </section>
  );
}
