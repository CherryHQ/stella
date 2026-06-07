import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  SettingsListHeader,
  SettingsListItem,
  SettingsListBody,
} from "@/features/settings/SettingsListPanel";
import type { ComponentsPublicChannel } from "@/lib/api-client/types.gen";
import { Plus } from "lucide-react";
import { siTelegram, siQq, siWechat } from "simple-icons";

function BrandIcon({ path, className = "size-4 shrink-0" }: { path: string; className?: string }) {
  return (
    <svg viewBox="0 0 24 24" className={className} fill="currentColor" aria-hidden="true">
      <path d={path} />
    </svg>
  );
}

const platformMeta: Record<string, { label: string; icon?: string }> = {
  telegram: { label: "Telegram", icon: siTelegram.path },
  qq: { label: "QQ", icon: siQq.path },
  feishu: { label: "Feishu" },
  weixin: { label: "Weixin", icon: siWechat.path },
};

// ─── admin list ──────────────────────────────────────────────────────────────

interface AdminChannel {
  id: string;
  name: string;
  type: string;
  enabled: boolean;
}

interface AdminChannelListPanelProps {
  channels: AdminChannel[];
  selectedId?: string;
  onSelect: (id: string) => void;
  onNew: () => void;
}

export function AdminChannelListPanel({
  channels,
  selectedId,
  onSelect,
  onNew,
}: AdminChannelListPanelProps) {
  const grouped = channels.reduce<Record<string, AdminChannel[]>>((acc, ch) => {
    const type = ch.type || "other";
    if (!acc[type]) acc[type] = [];
    acc[type].push(ch);
    return acc;
  }, {});

  const groups = Object.entries(grouped)
    .map(([type, items]) => {
      const meta = platformMeta[type];
      return { type, label: meta?.label || type, icon: meta?.icon, channels: items };
    })
    .sort((a, b) => a.label.localeCompare(b.label));

  return (
    <>
      <SettingsListHeader
        title="Channels"
        action={
          <Button onClick={onNew} variant="ghost" size="icon-sm">
            <Plus className="size-4" />
          </Button>
        }
      />
      <SettingsListBody>
        {groups.map((group) => (
          <div key={group.type} className="space-y-0.5">
            <div className="flex items-center gap-2 px-3 py-1.5">
              {group.icon && (
                <BrandIcon path={group.icon} className="size-3.5 shrink-0 text-muted-foreground" />
              )}
              <span className="text-xs font-medium text-muted-foreground">{group.label}</span>
              <Badge variant="secondary" size="sm">
                {group.channels.length}
              </Badge>
            </div>
            {group.channels.map((ch) => {
              const label = ch.name || platformMeta[ch.type]?.label || ch.type;
              return (
                <SettingsListItem
                  key={ch.id}
                  active={selectedId === ch.id}
                  onClick={() => onSelect(ch.id)}
                >
                  <div className="flex items-center gap-2">
                    <span
                      className={`shrink-0 size-1.5 rounded-full ${ch.enabled ? "bg-green-500" : "bg-muted-foreground"}`}
                    />
                    <span className="text-sm truncate">{label}</span>
                  </div>
                  <span className="text-xs text-muted-foreground font-mono">{ch.id}</span>
                </SettingsListItem>
              );
            })}
          </div>
        ))}
      </SettingsListBody>
    </>
  );
}

// ─── non-admin (public) list ─────────────────────────────────────────────────

interface PublicChannelListPanelProps {
  channels: ComponentsPublicChannel[];
  linkedPlatforms: Set<string>;
  selectedId?: string;
  onSelect: (type: string) => void;
}

export function PublicChannelListPanel({
  channels,
  linkedPlatforms,
  selectedId,
  onSelect,
}: PublicChannelListPanelProps) {
  const grouped = channels.reduce<Record<string, ComponentsPublicChannel[]>>((acc, ch) => {
    const type = ch.type || "other";
    if (!acc[type]) acc[type] = [];
    acc[type].push(ch);
    return acc;
  }, {});

  const groups = Object.entries(grouped)
    .map(([type, items]) => {
      const meta = platformMeta[type];
      return { type, label: meta?.label || type, icon: meta?.icon, channels: items };
    })
    .sort((a, b) => a.label.localeCompare(b.label));

  return (
    <>
      <SettingsListHeader title="Channels" />
      <SettingsListBody>
        {groups.map((group) => (
          <div key={group.type} className="space-y-0.5">
            <div className="flex items-center gap-2 px-3 py-1.5">
              {group.icon && (
                <BrandIcon path={group.icon} className="size-3.5 shrink-0 text-muted-foreground" />
              )}
              <span className="text-xs font-medium text-muted-foreground">{group.label}</span>
            </div>
            {group.channels.map((ch) => {
              const label = platformMeta[ch.type]?.label || ch.label || ch.type;
              const linked = linkedPlatforms.has(ch.type);
              return (
                <SettingsListItem
                  key={ch.type}
                  active={selectedId === ch.type}
                  onClick={() => onSelect(ch.type)}
                >
                  <div className="flex items-center gap-2">
                    <span
                      className={`shrink-0 size-1.5 rounded-full ${linked ? "bg-green-500" : "bg-muted-foreground"}`}
                    />
                    <span className="text-sm truncate">{label}</span>
                  </div>
                  <span className="text-xs text-muted-foreground">
                    {linked ? "Linked" : "Not linked"}
                  </span>
                </SettingsListItem>
              );
            })}
          </div>
        ))}
      </SettingsListBody>
    </>
  );
}
