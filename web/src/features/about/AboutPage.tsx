import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { useI18n } from "@/lib/i18n";

type StatusResponse = {
  status: string;
  version: string;
  commit?: string;
  buildDate?: string;
};

export function AboutPage() {
  const { t } = useI18n();
  const { data: status, isLoading } = useQuery({
    queryKey: ["status"],
    queryFn: () => api<StatusResponse>("GET", "/api/status"),
    retry: false,
  });

  return (
    <div>
      <div className="mb-6">
        <h1 className="font-serif text-3xl tracking-tight">{t("about.title")}</h1>
        <p className="text-muted-foreground text-sm mt-1">{t("about.description")}</p>
      </div>

      <div className="rounded-xl border border-border bg-card p-6 max-w-2xl">
        <dl className="divide-y divide-border">
          <InfoRow
            label={t("about.status")}
            value={isLoading ? t("common.loading") : status?.status}
          />
          <InfoRow
            label={t("about.version")}
            value={status?.version ? `v${status.version}` : "—"}
          />
          <InfoRow label={t("about.commit")} value={status?.commit || "—"} />
          <InfoRow label={t("about.buildDate")} value={status?.buildDate || "—"} />
        </dl>
      </div>
    </div>
  );
}

function InfoRow({ label, value }: { label: string; value?: string }) {
  return (
    <div className="grid grid-cols-3 gap-4 py-3 first:pt-0 last:pb-0">
      <dt className="text-sm font-medium text-muted-foreground">{label}</dt>
      <dd className="col-span-2 text-sm font-mono text-foreground break-all">{value || "—"}</dd>
    </div>
  );
}
