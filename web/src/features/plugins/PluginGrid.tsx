import type { ReactNode } from "react";
import { siGithub, siX } from "simple-icons";
import { Blocks, Cpu, Plug, Terminal, Webhook } from "lucide-react";
import type { PluginWithMeta } from "@/lib/types";
import type { PluginBucket } from "./pluginUtils";
import {
  pluginBucket,
  pluginHasOAuth,
  pluginIsEssential,
  pluginIsReleaseManaged,
  pluginLabel,
} from "./pluginUtils";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { SettingsCard, SettingsCardSection } from "@/features/settings/SettingsCardGrid";

function BrandIcon({ path }: { path: string }) {
  return (
    <svg viewBox="0 0 24 24" className="size-4" fill="currentColor" aria-hidden="true">
      <path d={path} />
    </svg>
  );
}

// PluginIcon shows a brand mark for known integrations, else a lucide glyph that
// reflects the plugin's bucket.
function PluginIcon({ plugin }: { plugin: PluginWithMeta }) {
  const provider = plugin._manifestPlugin?.oauth_provider ?? "";
  if (provider === "github" || plugin.id === "tool/gh") return <BrandIcon path={siGithub.path} />;
  if (provider === "x" || plugin.id === "tool/x") return <BrandIcon path={siX.path} />;

  const bucket = pluginBucket(plugin);
  const cls = "size-4";
  if (pluginHasOAuth(plugin)) return <Plug className={cls} />;
  if (bucket === "system")
    return plugin.kind === "hook" ? <Webhook className={cls} /> : <Cpu className={cls} />;
  if (bucket === "tool") return <Terminal className={cls} />;
  return <Blocks className={cls} />;
}

interface PluginCardProps {
  plugin: PluginWithMeta;
  active: boolean;
  detailRoute: "/admin/integrations/plugins/$pluginId";
  onToggle: (enabled: boolean) => void;
}

export function PluginCard({ plugin, active, detailRoute, onToggle }: PluginCardProps) {
  const essential = pluginIsEssential(plugin);
  const releaseManaged = pluginIsReleaseManaged(plugin);
  const oauthProvider = plugin._manifestPlugin?.oauth_provider;

  return (
    <SettingsCard
      icon={<PluginIcon plugin={plugin} />}
      title={pluginLabel(plugin)}
      badge={
        essential ? (
          <Badge variant="secondary" size="sm">
            core
          </Badge>
        ) : undefined
      }
      description={plugin.description || undefined}
      action={
        <Switch
          checked={plugin.enabled}
          disabled={essential || releaseManaged}
          onCheckedChange={(checked) => onToggle(checked)}
        />
      }
      footer={
        oauthProvider ? (
          <>
            <Plug className="size-3 text-muted-foreground" />
            <span className="text-xs text-muted-foreground">{oauthProvider}</span>
          </>
        ) : undefined
      }
      active={active}
      to={detailRoute}
      params={{ pluginId: plugin.name }}
    />
  );
}

interface PluginSectionProps {
  icon: ReactNode;
  title: string;
  description: string;
  plugins: PluginWithMeta[];
  activeName?: string;
  detailRoute: "/admin/integrations/plugins/$pluginId";
  onToggle: (plugin: PluginWithMeta, enabled: boolean) => void;
}

export function PluginSection({
  icon,
  title,
  description,
  plugins,
  activeName,
  detailRoute,
  onToggle,
}: PluginSectionProps) {
  if (plugins.length === 0) return null;
  return (
    <SettingsCardSection icon={icon} title={title} description={description} count={plugins.length}>
      {plugins.map((plugin) => (
        <PluginCard
          key={plugin.id}
          plugin={plugin}
          active={activeName === plugin.name}
          detailRoute={detailRoute}
          onToggle={(enabled) => onToggle(plugin, enabled)}
        />
      ))}
    </SettingsCardSection>
  );
}

export const bucketIcon: Record<PluginBucket, ReactNode> = {
  integration: <Plug className="size-4" />,
  tool: <Terminal className="size-4" />,
  system: <Cpu className="size-4" />,
};
