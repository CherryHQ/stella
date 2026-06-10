import type { ReactNode } from "react";
import { siGithub, siX } from "simple-icons";
import { Blocks, Cpu, Plug, Terminal, Webhook } from "lucide-react";
import type { PluginWithMeta } from "@/lib/types";
import type { PluginBucket } from "./pluginUtils";
import { pluginBucket, pluginHasOAuth, pluginIsEssential, pluginLabel } from "./pluginUtils";
import { Card } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";

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
  onSelect: () => void;
  onToggle: (enabled: boolean) => void;
}

export function PluginCard({ plugin, active, onSelect, onToggle }: PluginCardProps) {
  const essential = pluginIsEssential(plugin);
  const oauthProvider = plugin._manifestPlugin?.oauth_provider;

  return (
    <Card
      onClick={onSelect}
      className={`cursor-pointer p-4 gap-3 transition-colors hover:border-ring/40 ${
        active ? "border-ring/60" : ""
      }`}
    >
      <div className="flex items-start gap-3">
        <span className="grid size-9 shrink-0 place-items-center rounded-lg border border-border bg-muted text-muted-foreground">
          <PluginIcon plugin={plugin} />
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-1.5">
            <span className="truncate text-sm font-medium text-foreground">
              {pluginLabel(plugin)}
            </span>
            {essential && (
              <Badge variant="secondary" size="sm">
                core
              </Badge>
            )}
          </div>
          {plugin.description && (
            <p className="mt-0.5 line-clamp-2 text-xs text-muted-foreground">
              {plugin.description}
            </p>
          )}
        </div>
        <span
          className="shrink-0"
          onClick={(e) => e.stopPropagation()}
          onKeyDown={(e) => e.stopPropagation()}
          role="presentation"
        >
          <Switch
            checked={plugin.enabled}
            disabled={essential}
            onCheckedChange={(checked) => onToggle(checked)}
          />
        </span>
      </div>
      {oauthProvider && (
        <div className="flex items-center gap-1.5 border-t border-border pt-2.5">
          <Plug className="size-3 text-muted-foreground" />
          <span className="text-xs text-muted-foreground">{oauthProvider}</span>
        </div>
      )}
    </Card>
  );
}

interface PluginSectionProps {
  icon: ReactNode;
  title: string;
  description: string;
  plugins: PluginWithMeta[];
  activeName?: string;
  onSelect: (plugin: PluginWithMeta) => void;
  onToggle: (plugin: PluginWithMeta, enabled: boolean) => void;
}

export function PluginSection({
  icon,
  title,
  description,
  plugins,
  activeName,
  onSelect,
  onToggle,
}: PluginSectionProps) {
  if (plugins.length === 0) return null;
  return (
    <section className="space-y-3">
      <div className="flex items-center gap-2">
        <span className="text-muted-foreground">{icon}</span>
        <h2 className="text-sm font-semibold text-foreground">{title}</h2>
        <Badge variant="secondary" size="sm">
          {plugins.length}
        </Badge>
        <span className="text-xs text-muted-foreground">— {description}</span>
      </div>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {plugins.map((plugin) => (
          <PluginCard
            key={plugin.id}
            plugin={plugin}
            active={activeName === plugin.name}
            onSelect={() => onSelect(plugin)}
            onToggle={(enabled) => onToggle(plugin, enabled)}
          />
        ))}
      </div>
    </section>
  );
}

export const bucketIcon: Record<PluginBucket, ReactNode> = {
  integration: <Plug className="size-4" />,
  tool: <Terminal className="size-4" />,
  system: <Cpu className="size-4" />,
};
