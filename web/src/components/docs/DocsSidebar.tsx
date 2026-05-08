import { Link, useRouterState } from "@tanstack/react-router";
import type { SidebarGroup } from "@/lib/docs/docs";

export function DocsSidebar({ groups }: { groups: SidebarGroup[] }) {
  const pathname = useRouterState({ select: (s) => s.location.pathname });

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
    </nav>
  );
}
