import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { platformLabel } from "@/components/PlatformIcon";
import type { Channel } from "@/lib/types";
import { useI18n } from "@/lib/i18n";

// ─── platform metadata ────────────────────────────────────────────────────────

export type PlatformDefaults = Record<string, string | boolean>;

/**
 * The credential fields each platform stores on a channel row. This map is the
 * only definition of a channel's config shape: it drives the rendered fields,
 * the defaults a draft starts from, and — through `channelConfig` — exactly
 * which keys survive a save. A key absent here is dropped on the next write.
 */
export const platformDefaults: Record<string, PlatformDefaults> = {
  telegram: { token: "", channel_id: "" },
  qq: { app_id: "", app_secret: "" },
  feishu: {
    app_id: "",
    app_secret: "",
    encrypt_key: "",
    verification_token: "",
    tenant_key: "",
    auto_provision: false,
  },
  weixin: { bot_token: "", base_url: "", bot_id: "", user_id: "" },
  discord: {
    token: "",
    guild_id: "",
    channel_id: "",
    mention_only: true,
    respond_channels: "",
  },
};

export const channelTypes = Object.keys(platformDefaults).map((id) => ({
  id,
  label: platformLabel(id),
}));

export const defaultChannelType = channelTypes[0]?.id || "";

export function parseConfig(raw: string): Record<string, unknown> {
  try {
    return JSON.parse(raw || "{}");
  } catch {
    return {};
  }
}

export function platformConfigDefaults(type: string): PlatformDefaults {
  return { ...platformDefaults[type] };
}

function normalizeConfigValue(defaultValue: string | boolean, value: unknown): string | boolean {
  if (typeof defaultValue === "boolean") return Boolean(value);
  return (value as string) || "";
}

export function serializePlatformConfig(
  type: string,
  data: Record<string, unknown>,
): Record<string, unknown> {
  return Object.fromEntries(
    Object.entries(platformConfigDefaults(type)).map(([key, defaultValue]) => [
      key,
      normalizeConfigValue(defaultValue, data[key]),
    ]),
  );
}

export function hasConfig(type: string, data: Record<string, unknown>): boolean {
  return Object.values(serializePlatformConfig(type, data)).some((v) => {
    if (typeof v === "boolean") return v;
    return String(v).trim() !== "";
  });
}

export interface NormalizedChannel extends Record<string, unknown> {
  id: string;
  name: string;
  type: string;
  label?: string;
  agent_id: string;
  agent_name?: string;
  enabled: boolean;
}

/**
 * Flatten a stored channel into the draft every editor mutates: the config JSON
 * is spread onto the row so a field is one key, not a nested path.
 */
export function normalizeChannel(ch: Channel): NormalizedChannel {
  const type = ch.type || ch.id;
  return {
    ...ch,
    name: ch.name || "",
    type,
    agent_id: ch.agent_id || "",
    ...platformConfigDefaults(type),
    ...parseConfig(ch.config),
  };
}

/**
 * A ready-to-use display name for a new channel: `{type}-{4 hex chars}`. The id
 * is minted by the server and is a uuid nobody wants to read, so the name is the
 * only handle a user has on a channel — prefilling it means they can click
 * straight through, and it stays editable. The server applies the same default.
 */
export function suggestChannelName(type: string): string {
  const bytes = new Uint8Array(2);
  crypto.getRandomValues(bytes);
  const suffix = Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
  return `${type}-${suffix}`;
}

export function newInstanceDraft(
  type = defaultChannelType,
  name = suggestChannelName(type),
): Record<string, unknown> {
  return {
    type,
    name,
    ...platformConfigDefaults(type),
  };
}

/** The `config` string a write request carries: only the platform's own keys. */
export function channelConfig(ch: Record<string, unknown>): string {
  return JSON.stringify(serializePlatformConfig(ch.type as string, ch));
}

// ─── fields ───────────────────────────────────────────────────────────────────

/**
 * The per-platform credential inputs. Labels stay in the platform's own words
 * (`Bot Token`, `App ID`) because that is what the operator reads in the
 * platform's console — translating them would break the match.
 */
export function ChannelConfigFields({
  channel,
  onChange,
}: {
  channel: Record<string, unknown>;
  onChange: (key: string, value: unknown) => void;
}) {
  const { t } = useI18n();
  const type = channel.type as string;

  const field = (key: string, label: string, inputType = "text", placeholder = "") => (
    <Field key={key} className="w-full">
      <FieldLabel className="font-mono">{label}</FieldLabel>
      <Input
        nativeInput
        type={inputType}
        value={(channel[key] as string) || ""}
        onChange={(e) => onChange(key, e.target.value)}
        placeholder={placeholder}
        className="w-full font-mono"
      />
    </Field>
  );

  return (
    <div className="flex flex-col gap-4">
      {type === "telegram" && (
        <>
          {field("token", "Bot Token", "password", "From @BotFather")}
          {field("channel_id", "Channel ID", "text", "Default channel")}
        </>
      )}

      {type === "qq" && (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          {field("app_id", "App ID", "text", "QQ Bot App ID")}
          {field("app_secret", "App Secret", "password")}
        </div>
      )}

      {type === "feishu" && (
        <>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            {field("app_id", "App ID")}
            {field("app_secret", "App Secret", "password")}
            {field("encrypt_key", "Encrypt Key", "password", "optional")}
            {field("verification_token", "Verification Token", "password", "optional")}
            {field("tenant_key", "Tenant Key", "text", "optional, auto-detected at startup")}
          </div>
          <Field>
            <FieldLabel>{t("channels.autoProvision")}</FieldLabel>
            <Switch
              checked={Boolean(channel.auto_provision)}
              aria-label={t("channels.autoProvision")}
              onCheckedChange={(checked) => onChange("auto_provision", checked)}
            />
            <FieldDescription>{t("channels.autoProvisionDesc")}</FieldDescription>
          </Field>
        </>
      )}

      {type === "weixin" && (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          {field("bot_token", "Bot Token", "password")}
          {field("base_url", "Base URL", "text", "https://ilinkai.weixin.qq.com")}
          {field("bot_id", "Bot ID", "text", "optional")}
          {field("user_id", "User ID", "text", "optional")}
        </div>
      )}

      {type === "discord" && (
        <>
          {field("token", "Bot Token", "password", "From Discord Developer Portal")}
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            {field("guild_id", "Server ID", "text", "optional, restrict to one server")}
            {field("channel_id", "Channel ID", "text", "optional default channel")}
          </div>
          <Field>
            <FieldLabel>{t("channels.discordMentionOnly")}</FieldLabel>
            <Switch
              checked={Boolean(channel.mention_only)}
              aria-label={t("channels.discordMentionOnly")}
              onCheckedChange={(checked) => onChange("mention_only", checked)}
            />
            <FieldDescription>{t("channels.discordMentionOnlyDesc")}</FieldDescription>
          </Field>
          {field(
            "respond_channels",
            "Respond Channels",
            "text",
            "optional, comma-separated channel IDs that bypass mention-only",
          )}
        </>
      )}
    </div>
  );
}

/**
 * Everything an existing channel is edited by — name, enabled, credentials.
 * Shared so the settings inventory (`ChannelsPage`) and the agent profile's
 * channels tab ask for a channel in exactly the same words; neither owns the
 * binding, which is the agent page's dedicated bind endpoint.
 */
export function ChannelFields({
  channel,
  onChange,
}: {
  channel: NormalizedChannel;
  onChange: (key: string, value: unknown) => void;
}) {
  const { t } = useI18n();
  const label = platformLabel(channel.type);
  const hasConfigFields = Object.keys(platformConfigDefaults(channel.type)).length > 0;

  return (
    <div className="flex flex-col gap-4">
      <Field>
        <FieldLabel>{t("common.name")}</FieldLabel>
        <Input
          nativeInput
          type="text"
          value={channel.name || ""}
          onChange={(e) => onChange("name", (e.target as HTMLInputElement).value)}
          placeholder={label}
          className="w-full"
        />
      </Field>

      <Field>
        <FieldLabel>{t("channels.enabled")}</FieldLabel>
        <Switch
          checked={Boolean(channel.enabled)}
          aria-label={t("channels.enabled")}
          onCheckedChange={(checked) => onChange("enabled", checked)}
        />
        <FieldDescription>{t("channels.enabledDesc")}</FieldDescription>
      </Field>

      {hasConfigFields && <ChannelConfigFields channel={channel} onChange={onChange} />}
    </div>
  );
}
