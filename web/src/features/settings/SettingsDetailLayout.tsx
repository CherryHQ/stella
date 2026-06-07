import { useEffect, useState } from "react";
import type { ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { ArrowLeft } from "lucide-react";
import { useI18n } from "@/lib/i18n";

interface SettingsDetailLayoutProps {
  listHeader?: ReactNode;
  list: ReactNode;
  detail?: ReactNode;
  emptyState?: ReactNode;
  onBack?: () => void;
}

export function SettingsDetailLayout({
  listHeader,
  list,
  detail,
  emptyState,
  onBack,
}: SettingsDetailLayoutProps) {
  const { t } = useI18n();
  const hasDetail = detail !== undefined;
  const [mobileView, setMobileView] = useState<"list" | "detail">(hasDetail ? "detail" : "list");

  useEffect(() => {
    setMobileView(hasDetail ? "detail" : "list");
  }, [hasDetail]);

  return (
    <div className="flex h-full min-h-0 overflow-hidden bg-background">
      <div
        className={`${mobileView === "list" ? "flex" : "hidden"} w-full shrink-0 flex-col overflow-y-auto border-r border-border bg-card md:flex md:w-[240px]`}
      >
        <div className="shrink-0">{listHeader}</div>
        <div className="flex-1 overflow-y-auto">{list}</div>
      </div>

      <div
        className={`${mobileView === "detail" ? "flex" : "hidden"} min-w-0 flex-1 flex-col overflow-hidden bg-background md:flex`}
      >
        <Button
          variant="ghost"
          size="sm"
          onClick={() => {
            onBack?.();
            setMobileView("list");
          }}
          className="shrink-0 justify-start gap-1 border-b border-border rounded-none md:hidden"
        >
          <ArrowLeft className="size-4" />
          {t("common.back")}
        </Button>

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
