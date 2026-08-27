import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { targetValue } from "@/lib/utils";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { platformLabel } from "@/components/PlatformIcon";
import type { Channel } from "@/lib/types";
import { useI18n } from "@/lib/i18n";

// ─── platform metadata ────────────────────────────────────────────────────────

export interface PlatformDefaults {
  [key: string]: string | boolean | number | string[];
}

/**
 * The credential fields each platform stores on a channel row. This map is the
 * only definition of a channel's config shape: it drives the rendered fields,
 * the defaults a draft starts from, and — through `channelConfig` — exactly
 * which keys survive a save. A key absent here is dropped on the next write.
 */
export const platformDefaults = {
  telegram: {
    token: "",
    channel_id: "",
    allow_group: false,
    allowed_chat_ids: [],
    allowed_topic_ids: [],
    allow_dm: true,
    allow_unlinked_dm: false,
    guest_message_limit_per_minute: 10,
    guest_max_per_channel: 1000,
    guest_retention_days: 30,
    require_mention: true,
  },
  discord: {
    token: "",
    allow_group: false,
    allow_all_guilds: false,
    allowed_guild_ids: [],
    allowed_channel_ids: [],
    allowed_user_ids: [],
    allowed_role_ids: [],
    allow_dm: true,
    allow_unlinked_dm: false,
    guest_message_limit_per_minute: 10,
    guest_max_per_channel: 1000,
    guest_retention_days: 30,
    require_mention: true,
  },
  qq: { app_id: "", app_secret: "" },
  feishu: {
    app_id: "",
    app_secret: "",
    encrypt_key: "",
    verification_token: "",
    tenant_key: "",
    auto_provision: false,
    allow_group: false,
    allow_dm: true,
    allow_unlinked_dm: false,
    guest_message_limit_per_minute: 10,
    guest_max_per_channel: 1000,
    guest_retention_days: 30,
    require_mention: true,
  },
  dingtalk: {
    client_id: "",
    client_secret: "",
    allow_group: false,
    allow_dm: true,
    allow_unlinked_dm: false,
    guest_message_limit_per_minute: 10,
    guest_max_per_channel: 1000,
    guest_retention_days: 30,
    require_mention: true,
  },
  weixin: { bot_token: "", base_url: "", bot_id: "", user_id: "" },
} satisfies Record<string, PlatformDefaults>;

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
  const defaults = Object.entries(platformDefaults).find(([key]) => key === type)?.[1];
  return { ...defaults };
}

/** Splits comma- or newline-separated IDs, trimming blanks and duplicates. */
function splitIDList(value: unknown): string[] {
  const raw = Array.isArray(value) ? value.join(",") : typeof value === "string" ? value : "";
  const seen = new Set<string>();
  const out: string[] = [];
  for (const part of raw.split(/[,\n]/)) {
    const id = part.trim();
    if (!id || seen.has(id)) continue;
    seen.add(id);
    out.push(id);
  }
  return out;
}

function normalizeConfigValue(
  defaultValue: string | boolean | number | string[],
  value: unknown,
): string | boolean | number | string[] {
  if (typeof defaultValue === "boolean") return Boolean(value);
  if (typeof defaultValue === "number") {
    if (typeof value === "string" && value.trim() === "") return defaultValue;
    const number = Number(value);
    return Number.isFinite(number) ? Math.trunc(number) : defaultValue;
  }
  if (Array.isArray(defaultValue)) return splitIDList(value);
  // SAFETY: non-array single values are stored as strings in the channel draft.
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

export function newInstanceDraft(type = defaultChannelType, name = suggestChannelName(type)) {
  return {
    type,
    name,
    ...platformConfigDefaults(type),
  };
}

/** The `config` string a write request carries: only the platform's own keys. */
export function channelConfig(ch: Record<string, unknown>): string {
  // SAFETY: the config serialization only reads the platform discriminant.
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
  // SAFETY: channel.type carries the platform discriminant as a string.
  const type = channel.type as string;

  const field = (key: string, label: string, inputType = "text", placeholder = "") => {
    // SAFETY: scalar channel fields store their string form value.
    const stringValue = (channel[key] as string) ?? "";
    return (
      <Field key={key} className="w-full">
        <FieldLabel className="font-mono">{label}</FieldLabel>
        <Input
          nativeInput
          type={inputType}
          value={stringValue}
          onChange={(e) => onChange(key, e.target.value)}
          placeholder={placeholder}
          className="w-full font-mono"
        />
      </Field>
    );
  };

  /** A comma/newline editable text input that persists as a string array. */
  const arrayField = (key: string, label: string, description: string) => {
    const value = channel[key];
    // SAFETY: non-array values render as their string form; arrays join above.
    const display = Array.isArray(value) ? value.join(", ") : (value as string) || "";
    return (
      <Field key={key} className="w-full">
        <FieldLabel className="font-mono">{label}</FieldLabel>
        <Input
          nativeInput
          type="text"
          value={display}
          onChange={(e) => onChange(key, e.target.value)}
          placeholder="id-1, id-2"
          className="w-full font-mono"
        />
        <FieldDescription>{description}</FieldDescription>
      </Field>
    );
  };

  const numberField = (
    key: string,
    label: string,
    description: string,
    min: number,
    max: number,
  ) => {
    const value = channel[key];
    return (
      <Field key={key} className="w-full">
        <FieldLabel>{label}</FieldLabel>
        <Input
          nativeInput
          type="number"
          min={min}
          max={max}
          value={typeof value === "number" && Number.isFinite(value) ? value : ""}
          onChange={(e) => onChange(key, e.target.value === "" ? "" : e.target.valueAsNumber)}
          className="w-full font-mono"
        />
        <FieldDescription>{description}</FieldDescription>
      </Field>
    );
  };

  const accessFields = (groupLabel: string, groupDescription: string) => (
    <>
      <Field>
        <FieldLabel>{groupLabel}</FieldLabel>
        <Switch
          checked={Boolean(channel.allow_group)}
          aria-label={groupLabel}
          onCheckedChange={(checked) => onChange("allow_group", checked)}
        />
        <FieldDescription>{groupDescription}</FieldDescription>
      </Field>
      <Field>
        <FieldLabel>{t("channels.allowDm")}</FieldLabel>
        <Switch
          checked={Boolean(channel.allow_dm)}
          aria-label={t("channels.allowDm")}
          onCheckedChange={(checked) => onChange("allow_dm", checked)}
        />
        <FieldDescription>{t("channels.allowDmDesc")}</FieldDescription>
      </Field>
      <Field>
        <FieldLabel>{t("channels.allowUnlinkedDm")}</FieldLabel>
        <Switch
          checked={Boolean(channel.allow_unlinked_dm)}
          aria-label={t("channels.allowUnlinkedDm")}
          onCheckedChange={(checked) => onChange("allow_unlinked_dm", checked)}
        />
        <FieldDescription>{t("channels.allowUnlinkedDmDesc")}</FieldDescription>
      </Field>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        {numberField(
          "guest_message_limit_per_minute",
          t("channels.guestMessageLimit"),
          t("channels.guestMessageLimitDesc"),
          1,
          120,
        )}
        {numberField(
          "guest_max_per_channel",
          t("channels.guestMaxPerChannel"),
          t("channels.guestMaxPerChannelDesc"),
          1,
          100000,
        )}
        {numberField(
          "guest_retention_days",
          t("channels.guestRetentionDays"),
          t("channels.guestRetentionDaysDesc"),
          1,
          365,
        )}
      </div>
      <Field>
        <FieldLabel>{t("channels.requireMention")}</FieldLabel>
        <Switch
          checked={Boolean(channel.require_mention)}
          aria-label={t("channels.requireMention")}
          onCheckedChange={(checked) => onChange("require_mention", checked)}
        />
        <FieldDescription>{t("channels.requireMentionDesc")}</FieldDescription>
      </Field>
    </>
  );

  return (
    <div className="flex flex-col gap-4">
      {type === "telegram" && (
        <>
          {field("token", "Bot Token", "password", "From @BotFather")}
          {field("channel_id", "Channel ID", "text", "Default channel")}
          {accessFields(t("channels.allowGroup"), t("channels.allowGroupDesc"))}
          {arrayField(
            "allowed_chat_ids",
            t("channels.allowedTelegramChatIds"),
            t("channels.allowedTelegramChatIdsDesc"),
          )}
          {arrayField(
            "allowed_topic_ids",
            t("channels.allowedTelegramTopicIds"),
            t("channels.allowedTelegramTopicIdsDesc"),
          )}
        </>
      )}

      {type === "discord" && (
        <>
          {field("token", "Bot Token", "password", "Discord Developer Portal")}
          {accessFields(t("channels.allowGuild"), t("channels.allowGuildDesc"))}
          <Field>
            <FieldLabel>{t("channels.allowAllGuilds")}</FieldLabel>
            <Switch
              checked={Boolean(channel.allow_all_guilds)}
              aria-label={t("channels.allowAllGuilds")}
              onCheckedChange={(checked) => onChange("allow_all_guilds", checked)}
            />
            <FieldDescription>{t("channels.allowAllGuildsDesc")}</FieldDescription>
          </Field>
          {arrayField(
            "allowed_guild_ids",
            t("channels.allowedGuildIds"),
            t("channels.allowedGuildIdsDesc"),
          )}
          {arrayField(
            "allowed_channel_ids",
            t("channels.allowedChannelIds"),
            t("channels.allowedChannelIdsDesc"),
          )}
          {arrayField(
            "allowed_user_ids",
            t("channels.allowedUserIds"),
            t("channels.allowedUserIdsDesc"),
          )}
          {arrayField(
            "allowed_role_ids",
            t("channels.allowedRoleIds"),
            t("channels.allowedRoleIdsDesc"),
          )}
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
          {accessFields(t("channels.allowGroup"), t("channels.allowGroupDesc"))}
        </>
      )}

      {type === "dingtalk" && (
        <>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            {field("client_id", "Client ID", "text", "DingTalk application Client ID")}
            {field("client_secret", "Client Secret", "password")}
          </div>
          {accessFields(t("channels.allowGroup"), t("channels.allowGroupDesc"))}
          {/* A standalone note, not a field: Base UI's Description must stay inside a Field.Root. */}
          <p className="text-muted-foreground text-xs">{t("channels.dingtalkNotifyDesc")}</p>
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
          onChange={(e) =>
            // SAFETY: the target of a nativeInput change event is the input.
            onChange("name", targetValue(e))
          }
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
