// UnassignedPage — 未归属抓包：一个没有项目归属的会话桶。
//
// 它和项目空间是同一类东西（一个作用域下的会话集合），所以结构也一致：作用域头 + 同一份
// SessionList。这里不是"项目"，没有成员、插件、规则，也就没有配置页签。
import { Inbox, Play } from "lucide-react";
import { Button } from "@/components/ui/button";
import { SessionList } from "@/components/session-list";
import { WORKSPACE_HREF } from "@/lib/routes";
import { navigate } from "@/lib/router";
import type { Ref } from "react";

interface UnassignedPageProps {
  selectedSessionId: string | null;
  onSelectSession: (sessionId: string) => void;
  onDeletedSession: (sessionId: string) => void;
  onStartCapture: (projectId: string | null) => void;
  /** Ctrl/Cmd+K 聚焦这里的会话搜索框 */
  searchInputRef?: Ref<HTMLInputElement>;
}

export function UnassignedPage({
  selectedSessionId,
  onSelectSession,
  onDeletedSession,
  onStartCapture,
  searchInputRef,
}: UnassignedPageProps) {
  return (
    <div className="h-full overflow-auto gt-scroll">
      <div className="mx-auto max-w-4xl space-y-6 p-6">
        <header className="rounded-2xl border border-border bg-card/60 p-5">
          <div className="flex items-start justify-between gap-4">
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2">
                <Inbox className="h-4 w-4 shrink-0 text-muted-foreground" />
                <h1 className="text-lg font-semibold gt-gradient-text">未归属抓包</h1>
              </div>
              <p className="mt-1.5 text-sm text-muted-foreground">
                这些会话在开始抓包时没有选择归属项目，所以不属于任何项目空间。
              </p>
            </div>
            <div className="flex shrink-0 flex-col items-end gap-1.5">
              <Button size="sm" className="h-8" onClick={() => onStartCapture(null)}>
                <Play className="h-4 w-4" />
                开始抓包
              </Button>
              <button
                type="button"
                onClick={() => navigate(WORKSPACE_HREF)}
                className="text-micro text-muted-foreground hover:text-foreground hover:underline"
              >
                回到工作台
              </button>
            </div>
          </div>
        </header>

        <section>
          <h2 className="text-sm font-medium text-foreground">会话</h2>
          <div className="mt-2">
            <SessionList
              projectId={null}
              selectedSessionId={selectedSessionId}
              onSelectSession={onSelectSession}
              onDeleted={onDeletedSession}
              searchInputRef={searchInputRef}
            />
          </div>
        </section>
      </div>
    </div>
  );
}
