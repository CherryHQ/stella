import { useQuery } from "@tanstack/react-query";
import { Button } from "@/components/ui/button";
import { listAuthProviders } from "@/lib/api-client/sdk.gen";
import { useI18n } from "@/lib/i18n";
import { unwrapApiData } from "@/lib/api-data";
import type { OidcProviderList } from "@/lib/api-client/types.gen";

export function LoginPage() {
  const { t } = useI18n();

  const { data: providersData } = useQuery({
    queryKey: ["auth-providers"],
    queryFn: () => listAuthProviders({ throwOnError: true }),
    staleTime: 60_000,
  });
  const providers = unwrapApiData<OidcProviderList>(providersData?.data)?.items ?? [];

  return (
    <div className="min-h-screen flex items-center justify-center bg-background">
      <div className="w-full max-w-sm rounded-xl border border-border bg-card shadow-sm p-8">
        <div className="text-center mb-6">
          <span className="font-serif italic text-primary text-3xl tracking-tight select-none">
            stella
          </span>
          <p className="text-muted-foreground text-sm mt-1">{t("login.subtitle")}</p>
        </div>

        <div className="space-y-3">
          {providers.map((p) => (
            <a key={p.name} href={p.login_url} className="block">
              <Button type="button" variant="outline" className="w-full">
                {p.name === "local" ? t("login.signIn") : `${t("login.signIn")} ${p.name}`}
              </Button>
            </a>
          ))}
          {providers.length === 0 && (
            <p className="text-center text-sm text-muted-foreground">{t("login.noProviders")}</p>
          )}
          {providers.some((p) => p.register_url) && (
            <p className="text-center text-sm text-muted-foreground">
              {t("login.noAccount")}{" "}
              <a
                href={providers.find((p) => p.register_url)?.register_url}
                className="text-primary hover:underline"
              >
                {t("login.signUpLink")}
              </a>
            </p>
          )}
        </div>
      </div>
    </div>
  );
}
