import { useState } from "react";
import { RefreshCw, X } from "lucide-react";
import { Alert, AlertAction, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { useBuildUpdate } from "@/hooks/use-build-update";
import { useI18n } from "@/lib/i18n";

/**
 * Offers a reload once the server has moved to a newer build.
 *
 * Routing is client-side, so an open tab never re-fetches the document on its
 * own and can keep running an old bundle for days. Left alone it only corrects
 * itself by accident, when the user happens to route into a code-split chunk
 * the new build no longer serves.
 *
 * Dismissal is keyed to the build it dismissed, so ignoring one update does not
 * silence the next one.
 */
export function AppUpdateNotice() {
  const { t } = useI18n();
  const { stale, build } = useBuildUpdate();
  const [dismissed, setDismissed] = useState<string | null>(null);

  if (!stale || build === null || dismissed === build) return null;

  return (
    <div className="fixed inset-x-4 bottom-4 z-50 flex justify-center sm:inset-x-auto sm:right-4">
      <Alert variant="info" className="w-full sm:w-auto sm:max-w-sm">
        <RefreshCw />
        <AlertTitle>{t("update.available")}</AlertTitle>
        <AlertAction>
          <Button size="sm" onClick={() => window.location.reload()}>
            {t("update.reload")}
          </Button>
          <Button
            size="icon-sm"
            variant="ghost"
            aria-label={t("update.dismiss")}
            onClick={() => setDismissed(build)}
          >
            <X />
          </Button>
        </AlertAction>
      </Alert>
    </div>
  );
}
