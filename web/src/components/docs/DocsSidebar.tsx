import { ExternalLink } from "lucide-react";
import { Link, useRouterState } from "@tanstack/react-router";
import type { SidebarGroup } from "@/lib/docs/docs";
import { useI18n } from "@/lib/i18n";

export function DocsSidebar({ groups }: { groups: SidebarGroup[] }) {
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const { t } = useI18n();

  return (
    <nav className="space-y-6">
      {groups.map((group) => (
        <div key={group.title}>
          <h4 className="text-sm font-semibold text-foreground mb-2">{group.title}</h4>
          <ul className="space-y-0.5">
            {group.items.map((item) => {
              const active = pathname === item.href;
              return (
                <li key={item.slug}>
                  <Link
                    to={item.href as never}
                    className={`block text-sm px-3 py-1.5 rounded-md transition-colors ${
                      active
                        ? "bg-accent text-foreground font-medium"
                        : "text-muted-foreground hover:text-foreground hover:bg-accent/50"
                    }`}
                  >
                    {item.title}
                  </Link>
                </li>
              );
            })}
          </ul>
        </div>
      ))}
      <div>
        <a
          href="/api-references"
          target="_blank"
          rel="noopener noreferrer"
          className="flex items-center gap-1.5 text-sm px-3 py-1.5 rounded-md text-muted-foreground hover:text-foreground hover:bg-accent/50 transition-colors"
        >
          {t("nav.apiReferences")}
          <ExternalLink className="size-3" />
        </a>
      </div>
    </nav>
  );
}
