import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useRouterState } from "@tanstack/react-router";
import { useSidebar } from "@/components/ui/sidebar";
import { useI18n } from "@/lib/i18n";
import { groupsQueryOptions } from "@/lib/queries/groups";
import { SidebarItem, SidebarSection } from "@/components/AppSidebar";
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

export function GroupSection() {
  const { t } = useI18n();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const { setOpenMobile } = useSidebar();

  const [open, setOpen] = useState(true);
  const [showCreate, setShowCreate] = useState(false);

  const { data: groups = [] } = useQuery(groupsQueryOptions);

  const activeGroupId = pathname.match(/\/groups\/([^/]+)/)?.[1] ?? "";

  return (
    <>
      <SidebarSection
        title={t("groups.title")}
        open={open}
        onOpenChange={setOpen}
        action={
          <button
            type="button"
            onClick={() => setShowCreate(true)}
            className="grid size-6 place-items-center rounded-lg text-muted-foreground opacity-60 transition-all hover:bg-foreground/[0.055] hover:text-foreground hover:opacity-100"
            title={t("groups.newGroup")}
          >
            <IconPlus />
          </button>
        }
      >
        {groups.map((g) => {
          const isActive = activeGroupId === g.id;
          return (
            <SidebarItem
              key={g.id}
              active={isActive}
              icon={<IconGroup />}
              label={g.group_name || t("groups.unnamed")}
              to="/groups/$groupId"
              params={{ groupId: g.id }}
              onClick={() => setOpenMobile(false)}
            />
          );
        })}
        {groups.length === 0 && (
          <p className="px-2 py-2 font-mono text-xs text-muted-foreground">
            {t("groups.noGroups")}
          </p>
        )}
      </SidebarSection>
      <CreateGroupDialog open={showCreate} onClose={() => setShowCreate(false)} />
    </>
  );
}
