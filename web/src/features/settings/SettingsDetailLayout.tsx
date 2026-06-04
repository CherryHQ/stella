import { useEffect, useState } from "react";
import type { ReactNode } from "react";

interface SettingsDetailLayoutProps {
  listHeader?: ReactNode;
  list: ReactNode;
  detail?: ReactNode;
  emptyState?: ReactNode;
}

export function SettingsDetailLayout({
  listHeader,
  list,
  detail,
  emptyState,
}: SettingsDetailLayoutProps) {
  const hasDetail = detail !== undefined;
  const [mobileView, setMobileView] = useState<"list" | "detail">(hasDetail ? "detail" : "list");

  useEffect(() => {
    setMobileView(hasDetail ? "detail" : "list");
  }, [hasDetail]);

  return (
    <div className="flex h-full min-h-0 overflow-hidden bg-background">
      {/* Left list panel — full width on mobile when mobileView=list, hidden otherwise */}
      <div
        className={`${mobileView === "list" ? "flex" : "hidden"} w-full shrink-0 flex-col overflow-y-auto border-r border-border bg-card md:flex md:w-[240px]`}
      >
        <div className="shrink-0">{listHeader}</div>
        <div className="flex-1 overflow-y-auto">{list}</div>
      </div>

      {/* Right detail panel — full width on mobile when mobileView=detail, hidden otherwise */}
      <div
        className={`${mobileView === "detail" ? "flex" : "hidden"} min-w-0 flex-1 flex-col overflow-hidden bg-background md:flex`}
      >
        {/* Mobile back button */}
        <button
          onClick={() => setMobileView("list")}
          className="flex shrink-0 items-center gap-1 border-b border-border bg-card px-4 py-2 text-sm text-muted-foreground transition-colors duration-120 hover:bg-muted hover:text-foreground md:hidden cursor-pointer"
        >
          <svg
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.8"
            className="w-4 h-4"
          >
            <path strokeLinecap="round" strokeLinejoin="round" d="M15.75 19.5 8.25 12l7.5-7.5" />
          </svg>
          Back
        </button>

        <div className="flex-1 min-h-0 flex flex-col">
          {detail ??
            (emptyState ? (
              <div className="flex h-full items-center justify-center">{emptyState}</div>
            ) : null)}
        </div>
      </div>
    </div>
  );
}
