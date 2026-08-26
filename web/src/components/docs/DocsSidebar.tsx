import { ChevronRight, ExternalLink } from "lucide-react";
import { useState, useMemo } from "react";
import { Link, useRouterState } from "@tanstack/react-router";
import type { SidebarGroup, SidebarSection } from "@/lib/docs/docs";
import { useI18n } from "@/lib/i18n";

function filterIndex(items: SidebarGroup["items"]) {
  return items.filter((item) => !item.slug.endsWith("/index"));
}

function sectionContainsPath(section: SidebarSection, pathname: string) {
  return section.groups.some((g) => g.items.some((i) => pathname === i.href));
}

export function DocsSidebar({ sections }: { sections: SidebarSection[] }) {
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const { t } = useI18n();

  const activeSectionIndex = useMemo(() => {
    const idx = sections.findIndex((s) => sectionContainsPath(s, pathname));
    return idx >= 0 ? idx : 0;
  }, [sections, pathname]);

  const activeGroupTitle = useMemo(() => {
    for (const section of sections) {
      for (const group of section.groups) {
        if (group.items.some((item) => pathname === item.href)) return group.title;
      }
    }
    return null;
  }, [sections, pathname]);

  return (
    <nav className="space-y-4">
      {sections.map((section, si) => {
        const isMulti = section.groups.length > 1;
        const group = !isMulti ? section.groups[0] : null;
        const items = group ? filterIndex(group.items) : [];

        if (!isMulti && items.length <= 1) {
          const item = items[0];
          if (!item) return null;
          // SAFETY: item.href is a validated internal route, coerced to Link's route-union type.
          const itemTo = item.href as never;
          return (
            <div key={si}>
              <Link
                to={itemTo}
                className={`block text-sm font-semibold px-1 py-1 rounded-md transition-colors ${
                  pathname === item.href
                    ? "text-foreground"
                    : "text-muted-foreground hover:text-foreground"
                }`}
              >
                {section.title ?? item.title}
              </Link>
            </div>
          );
        }

        if (!isMulti && group) {
          return (
            <CollapsibleSection
              key={si}
              title={section.title ?? group.title}
              defaultOpen={si === activeSectionIndex}
            >
              <ul className="space-y-0.5">
                {items.map((item) => (
                  <SidebarLink key={item.slug} item={item} active={pathname === item.href} />
                ))}
              </ul>
            </CollapsibleSection>
          );
        }

        return (
          <CollapsibleSection
            key={si}
            title={section.title ?? ""}
            defaultOpen={si === activeSectionIndex}
          >
            <div className="space-y-1">
              {section.groups.map((g) => (
                <CollapsibleGroup
                  key={g.title}
                  group={g}
                  pathname={pathname}
                  defaultOpen={g.title === activeGroupTitle}
                />
              ))}
            </div>
          </CollapsibleSection>
        );
      })}
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

function CollapsibleSection({
  title,
  defaultOpen,
  children,
}: {
  title: string;
  defaultOpen: boolean;
  children: React.ReactNode;
}) {
  const [open, setOpen] = useState(defaultOpen);

  return (
    <div>
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center gap-1 text-sm font-semibold text-foreground px-1 py-1 rounded-md hover:bg-accent/50 transition-colors"
      >
        <ChevronRight
          className={`size-3.5 shrink-0 transition-transform ${open ? "rotate-90" : ""}`}
        />
        {title}
      </button>
      {open && <div className="mt-1">{children}</div>}
    </div>
  );
}

function CollapsibleGroup({
  group,
  pathname,
  defaultOpen,
}: {
  group: SidebarGroup;
  pathname: string;
  defaultOpen: boolean;
}) {
  const [open, setOpen] = useState(defaultOpen);
  const items = filterIndex(group.items);
  const hasActive = items.some((item) => pathname === item.href);

  return (
    <div>
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className={`flex w-full items-center gap-1 text-sm px-2 py-1 rounded-md transition-colors ${
          hasActive ? "font-medium text-foreground" : "text-muted-foreground hover:text-foreground"
        }`}
      >
        <ChevronRight
          className={`size-3 shrink-0 transition-transform ${open ? "rotate-90" : ""}`}
        />
        {group.title}
      </button>
      {open && (
        <ul className="space-y-0.5 mt-0.5">
          {items.map((item) => (
            <SidebarLink key={item.slug} item={item} active={pathname === item.href} />
          ))}
        </ul>
      )}
    </div>
  );
}

function SidebarLink({
  item,
  active,
}: {
  item: { slug: string; title: string; href: string };
  active: boolean;
}) {
  // SAFETY: item.href is a validated internal route, coerced to Link's route-union type.
  const itemTo = item.href as never;
  return (
    <li>
      <Link
        to={itemTo}
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
}
