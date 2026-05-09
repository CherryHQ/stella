import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { useI18n } from "@/lib/i18n";

type StatusResponse = {
  status: string;
  version: string;
  commit?: string;
  buildDate?: string;
  uptimeSeconds?: number;
  runtime?: {
    goVersion: string;
    os: string;
    arch: string;
    goroutines: number;
    memory: {
      allocBytes: number;
      heapAllocBytes: number;
      sysBytes: number;
      numGC: number;
    };
  };
  database?: {
    status: string;
    latencyMs?: number;
    error?: string;
  };
  plugins?: {
    total: number;
    enabled: number;
    disabled: number;
  };
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

      <div className="rounded-xl border border-border bg-card p-6 max-w-2xl mb-6">
        <h2 className="font-serif text-xl mb-4">{t("about.versionInfo")}</h2>
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
          <InfoRow label={t("about.uptime")} value={formatDuration(status?.uptimeSeconds)} />
        </dl>
      </div>

      {(status?.runtime || status?.database || status?.plugins) && (
        <div className="rounded-xl border border-border bg-card p-6 max-w-2xl">
          <h2 className="font-serif text-xl mb-4">{t("about.adminInfo")}</h2>
          <dl className="divide-y divide-border">
            {status.database && (
              <InfoRow
                label={t("about.database")}
                value={
                  status.database.latencyMs == null
                    ? status.database.status
                    : `${status.database.status} · ${status.database.latencyMs.toFixed(2)}ms`
                }
              />
            )}
            {status.runtime && (
              <>
                <InfoRow
                  label={t("about.runtime")}
                  value={`${status.runtime.goVersion} · ${status.runtime.os}/${status.runtime.arch}`}
                />
                <InfoRow label={t("about.goroutines")} value={String(status.runtime.goroutines)} />
                <InfoRow
                  label={t("about.memory")}
                  value={`${formatBytes(status.runtime.memory.heapAllocBytes)} heap · ${formatBytes(status.runtime.memory.sysBytes)} sys · ${status.runtime.memory.numGC} GC`}
                />
              </>
            )}
            {status.plugins && (
              <InfoRow
                label={t("about.plugins")}
                value={`${status.plugins.enabled}/${status.plugins.total} ${t("about.pluginsEnabled")}`}
              />
            )}
          </dl>
        </div>
      )}
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

function formatBytes(bytes?: number) {
  if (bytes == null) return undefined;
  const units = ["B", "KB", "MB", "GB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return `${value.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}

function formatDuration(seconds?: number) {
  if (seconds == null) return undefined;
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor((seconds % 86400) / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  if (days > 0) return `${days}d ${hours}h`;
  if (hours > 0) return `${hours}h ${minutes}m`;
  return `${minutes}m`;
}
