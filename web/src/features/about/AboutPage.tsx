import { useQuery } from "@tanstack/react-query";
import { getStatus } from "@/lib/api-client/sdk.gen";
import { useI18n } from "@/lib/i18n";
import { SettingsPageHeader } from "@/features/settings/SettingsPageHeader";

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
    queryFn: async () => {
      const { data } = await getStatus({ throwOnError: true });
      // SAFETY: getStatus returns the status report under data.
      return data as StatusResponse;
    },
    retry: false,
  });

  return (
    <div className="h-full overflow-y-auto bg-background">
      <div className="mx-auto max-w-3xl p-6 sm:p-8 lg:p-10 space-y-8">
        <SettingsPageHeader title={t("about.title")} description={t("about.description")} />

        <section>
          <h2 className="text-base font-semibold text-foreground mb-3">{t("about.versionInfo")}</h2>
          <div className="rounded-xl border border-border bg-card p-6">
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
        </section>

        {(status?.runtime || status?.database || status?.plugins) && (
          <section>
            <h2 className="text-base font-semibold text-foreground mb-3">{t("about.adminInfo")}</h2>
            <div className="rounded-xl border border-border bg-card p-6">
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
                    <InfoRow
                      label={t("about.goroutines")}
                      value={String(status.runtime.goroutines)}
                    />
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
          </section>
        )}
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
