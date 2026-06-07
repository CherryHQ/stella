import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useNavigate, useRouterState } from "@tanstack/react-router";
import { useSidebar } from "@/components/ui/sidebar";
import { useI18n } from "@/lib/i18n";
import { groupsQueryOptions } from "@/lib/queries/groups";
import { cn } from "@/lib/utils";
import { CreateGroupDialog } from "./CreateGroupDialog";

function IconGroup() {
  return (
    <svg
      className="size-3.5"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
    >
      <circle cx="9" cy="7" r="3" />
      <circle cx="17" cy="7" r="2" />
      <path d="M3 21v-2a4 4 0 0 1 4-4h4a4 4 0 0 1 4 4v2" />
      <path d="M21 21v-1.5a3 3 0 0 0-2-2.83" />
    </svg>
  );
}

function IconPlus() {
  return (
    <svg className="size-3" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5">
      <path d="M12 5v14M5 12h14" />
    </svg>
  );
}

function ChevRight({ className }: { className?: string }) {
  return (
    <svg
      className={className}
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
    >
      <path d="m6 4 4 4-4 4" />
    </svg>
  );
}

export function GroupSection() {
  const navigate = useNavigate();
  const { t } = useI18n();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const { setOpenMobile } = useSidebar();

  const [open, setOpen] = useState(true);
  const [showCreate, setShowCreate] = useState(false);

  const { data: groups = [] } = useQuery(groupsQueryOptions);

  const activeGroupId = pathname.match(/\/groups\/([^/]+)/)?.[1] ?? "";

  return (
    <>
      <section className="mt-3">
        <div className="flex h-[30px] items-center gap-1 pr-1">
          <button
            type="button"
            onClick={() => setOpen((v) => !v)}
            className="flex min-w-0 flex-1 items-center gap-2 rounded-[9px] px-2 py-1 font-mono text-[10px] text-muted-foreground hover:bg-foreground/[0.045] hover:text-muted-foreground"
          >
            <span>{t("groups.title")}</span>
            <ChevRight
              className={cn(
                "size-2.5 text-muted-foreground transition-transform duration-150",
                open && "rotate-90",
              )}
            />
          </button>
          <button
            type="button"
            onClick={() => setShowCreate(true)}
            className="grid size-6 place-items-center rounded-lg text-muted-foreground opacity-60 transition-all hover:bg-foreground/[0.055] hover:text-foreground hover:opacity-100"
            title={t("groups.newGroup")}
          >
            <IconPlus />
          </button>
        </div>
        {open && (
          <div className="grid gap-px">
            {groups.map((g) => {
              const isActive = activeGroupId === g.id;
              return (
                <button
                  key={g.id}
                  type="button"
                  onClick={() => {
                    setOpenMobile(false);
                    void navigate({ to: "/groups/$groupId", params: { groupId: g.id } });
                  }}
                  className={cn(
                    "grid min-h-[32px] w-full grid-cols-[18px_minmax(0,1fr)] items-center gap-2 rounded-xl px-[7px] text-left text-[13px] font-medium tracking-[-0.016em] transition-colors border",
                    isActive
                      ? "bg-muted text-foreground border-border"
                      : "text-muted-foreground hover:bg-muted hover:text-foreground border-transparent",
                  )}
                >
                  <span className={cn("opacity-90", isActive && "text-foreground")}>
                    <IconGroup />
                  </span>
                  <span className="truncate">{g.group_name || t("groups.unnamed")}</span>
                </button>
              );
            })}
            {groups.length === 0 && (
              <p className="px-2 py-2 font-mono text-xs text-muted-foreground">
                {t("groups.noGroups")}
              </p>
            )}
          </div>
        )}
      </section>
      <CreateGroupDialog open={showCreate} onClose={() => setShowCreate(false)} />
    </>
  );
}
