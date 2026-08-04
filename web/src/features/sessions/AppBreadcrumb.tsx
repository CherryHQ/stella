import { Link } from "@tanstack/react-router";
import { ListTodo, MoreHorizontal, Puzzle, UserRound } from "lucide-react";
import { useI18n } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from "@/components/ui/breadcrumb";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/menu";

/**
 * L2 header: where you are, and the only way into the profile pages behind the
 * conversation. Clicking a name opens that thing's profile — the conversation
 * itself is always the default view, so nothing above it needs a tab strip.
 */
export function AppBreadcrumb({
  agentId,
  agentName,
  projectId,
  projectName,
}: {
  agentId: string;
  agentName: string;
  projectId?: string;
  projectName?: string;
}) {
  const inProject = !!projectId && !!projectName;

  return (
    <Breadcrumb className="min-w-0">
      <BreadcrumbList>
        <BreadcrumbItem>
          <BreadcrumbLink render={<Link to="/agents/$agentId/profile" params={{ agentId }} />}>
            <span className="truncate font-medium text-foreground">{agentName}</span>
          </BreadcrumbLink>
        </BreadcrumbItem>
        {inProject && (
          <>
            <BreadcrumbSeparator />
            <BreadcrumbItem>
              <BreadcrumbLink
                render={
                  <Link
                    to="/agents/$agentId/projects/$projectId/profile"
                    params={{ agentId, projectId }}
                  />
                }
              >
                <span className="truncate font-medium text-foreground">{projectName}</span>
              </BreadcrumbLink>
            </BreadcrumbItem>
          </>
        )}
      </BreadcrumbList>
    </Breadcrumb>
  );
}

/**
 * The "⋯" menu behind the breadcrumb, rendered in the header's static actions
 * slot so page-level actions can never displace it.
 */
export function AppHeaderMenu({ agentId, projectId }: { agentId: string; projectId?: string }) {
  const { t } = useI18n();
  const inProject = !!projectId;

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={<Button variant="ghost" size="icon-sm" aria-label={t("profile.more")} />}
      >
        <MoreHorizontal />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" sideOffset={6}>
        <DropdownMenuItem
          render={
            inProject && projectId ? (
              <Link
                to="/agents/$agentId/projects/$projectId/profile"
                params={{ agentId, projectId }}
              />
            ) : (
              <Link to="/agents/$agentId/profile" params={{ agentId }} />
            )
          }
        >
          <UserRound className="size-4" />
          {t("profile.title")}
        </DropdownMenuItem>
        <DropdownMenuItem render={<Link to="/agents/$agentId/goals" params={{ agentId }} />}>
          <ListTodo className="size-4" />
          {t("sidebar.goals")}
        </DropdownMenuItem>
        <DropdownMenuItem
          render={
            inProject ? (
              <Link
                to="/agents/$agentId/projects/$projectId/skills"
                params={{ agentId, projectId }}
              />
            ) : (
              <Link to="/agents/$agentId/skills" params={{ agentId }} />
            )
          }
        >
          <Puzzle className="size-4" />
          {t("profile.skills")}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/** Group conversations have no profile page yet — just the name and the count. */
export function GroupBreadcrumb({ name, memberCount }: { name: string; memberCount: number }) {
  const { t } = useI18n();
  return (
    <Breadcrumb className="min-w-0">
      <BreadcrumbList>
        <BreadcrumbItem>
          <BreadcrumbPage className="truncate font-medium">{name}</BreadcrumbPage>
        </BreadcrumbItem>
        <BreadcrumbSeparator />
        <BreadcrumbItem>
          <span className="text-xs text-muted-foreground">
            {t("groups.memberCount", { count: memberCount })}
          </span>
        </BreadcrumbItem>
      </BreadcrumbList>
    </Breadcrumb>
  );
}
