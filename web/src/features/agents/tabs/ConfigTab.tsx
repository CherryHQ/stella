import { useRef, useState } from "react";
import type { Channel } from "@/lib/api-client";
import type { AgentsPageState, ModelOption } from "../agent-detail-state";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { useI18n } from "@/lib/i18n";

interface Props {
  state: AgentsPageState;
  onSetState: (patch: Partial<AgentsPageState>) => void;
}

const thinkingLevels = ["", "minimal", "low", "medium", "high", "xhigh"] as const;

function ThinkingField({
  label,
  value,
  onChange,
}: {
  label: string;
  value: string;
  onChange: (val: string) => void;
}) {
  const { t } = useI18n();
  return (
    <div>
      <label className="block text-xs font-semibold text-muted-foreground mb-1.5">{label}</label>
      <select
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
      >
        {thinkingLevels.map((level) => (
          <option key={level || "default"} value={level}>
            {level || t("agents.form.thinkingDefault")}
          </option>
        ))}
      </select>
    </div>
  );
}

function ModelComboField({
  label,
  field,
  value,
  placeholder,
  optional,
  cachedModels,
  onChange,
}: {
  label: string;
  field: string;
  value: string;
  placeholder: string;
  optional?: boolean;
  cachedModels: ModelOption[];
  onChange: (val: string) => void;
}) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");

  const filtered = search
    ? cachedModels.filter((m) => m.label.toLowerCase().includes(search.toLowerCase()))
    : cachedModels;

  const displayValue = cachedModels.find((m) => m.value === value)?.label ?? value;

  return (
    <div className="relative">
      <div className="flex items-center justify-between mb-1.5">
        <label
          className="text-xs font-semibold text-muted-foreground"
          htmlFor={`model-field-${field}`}
        >
          {label}
        </label>
        {optional && (
          <span className="text-xs font-mono text-muted-foreground">({t("common.optional")})</span>
        )}
      </div>
      <Input
        nativeInput
        type="text"
        value={open ? search || displayValue : displayValue}
        onChange={(e) => {
          const v = (e.target as HTMLInputElement).value;
          setSearch(v);
          onChange(v);
          setOpen(cachedModels.length > 0);
        }}
        onFocus={() => {
          setSearch("");
          setOpen(cachedModels.length > 0);
        }}
        onBlur={() => setTimeout(() => setOpen(false), 150)}
        placeholder={placeholder}
        className="text-sm font-mono"
        autoComplete="off"
        id={`model-field-${field}`}
      />
      {open && filtered.length > 0 && (
        <div className="absolute z-20 mt-1 w-full max-h-48 overflow-y-auto bg-popover border border-border rounded-xl py-1">
          {filtered.map((m) => (
            <button
              key={m.value}
              onMouseDown={() => {
                onChange(m.value);
                setOpen(false);
              }}
              type="button"
              className={`w-full text-left px-3 py-1.5 text-xs font-mono hover:bg-muted/80 cursor-pointer transition-colors duration-120 ${
                value === m.value ? "text-primary font-semibold" : "text-muted-foreground"
              }`}
            >
              {m.label}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

const platformLabels: Record<string, string> = {
  telegram: "Telegram",
  qq: "QQ",
  feishu: "Feishu",
  weixin: "Weixin",
};

function channelDisplayName(ch: Channel): string {
  return ch.name || platformLabels[ch.type] || ch.type;
}

function ChannelSelector({
  channels,
  selectedIds,
  onToggle,
  onRemove,
}: {
  channels: Channel[];
  selectedIds: string[];
  onToggle: (id: string) => void;
  onRemove: (id: string) => void;
}) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);
  const unselected = channels.filter((ch) => !selectedIds.includes(ch.id));
  const selected = channels.filter((ch) => selectedIds.includes(ch.id));

  return (
    <div>
      <label className="block text-xs font-semibold text-muted-foreground mb-1">
        {t("agents.form.channels")}
      </label>
      <p className="text-xs text-muted-foreground mb-2">{t("agents.form.channelsDesc")}</p>
      <div ref={containerRef} className="relative">
        <div
          className="min-h-9 w-full rounded-lg border border-border bg-background px-3 py-1.5 text-sm cursor-pointer flex flex-wrap gap-1.5 items-center"
          onClick={() => setOpen((v) => !v)}
          onKeyDown={(e) => e.key === "Enter" && setOpen((v) => !v)}
          role="combobox"
          aria-expanded={open}
          tabIndex={0}
        >
          {selected.length === 0 && (
            <span className="text-muted-foreground">{t("agents.form.selectChannels")}</span>
          )}
          {selected.map((ch) => (
            <span
              key={ch.id}
              className="inline-flex items-center gap-1 rounded-md bg-muted border border-border px-2 py-0.5 text-xs font-mono text-foreground"
            >
              {channelDisplayName(ch)}
              <button
                type="button"
                onClick={(e) => {
                  e.stopPropagation();
                  onRemove(ch.id);
                }}
                className="text-muted-foreground hover:text-foreground ml-0.5 cursor-pointer font-semibold"
              >
                ×
              </button>
            </span>
          ))}
        </div>
        {open && (
          <>
            <div className="fixed inset-0 z-10" onClick={() => setOpen(false)} />
            <div className="absolute z-20 mt-1 w-full max-h-48 overflow-y-auto bg-popover border border-border rounded-xl py-1">
              {unselected.length === 0 && (
                <div className="px-3 py-2 text-xs text-muted-foreground">
                  No more channels available.
                </div>
              )}
              {unselected.map((ch) => (
                <button
                  key={ch.id}
                  type="button"
                  onMouseDown={() => {
                    onToggle(ch.id);
                  }}
                  className="w-full text-left px-3 py-1.5 hover:bg-muted/80 cursor-pointer flex items-center justify-between transition-colors duration-120"
                >
                  <div className="min-w-0">
                    <p className="text-sm font-medium text-foreground truncate">
                      {channelDisplayName(ch)}
                    </p>
                    <p className="text-xs text-muted-foreground font-mono truncate">{ch.type}</p>
                  </div>
                </button>
              ))}
            </div>
          </>
        )}
      </div>
    </div>
  );
}

export function ConfigTab({ state, onSetState }: Props) {
  const { t } = useI18n();
  const { form, cachedModels, isAdmin, editingId, channels, selectedChannelIDs } = state;

  const setForm = (patch: Partial<typeof form>) => onSetState({ form: { ...form, ...patch } });

  const availableChannels = channels.filter(
    (ch) => ch.enabled && (!ch.agent_id || ch.agent_id === editingId),
  );

  const toggleChannel = (chId: string) => {
    const ids = selectedChannelIDs.includes(chId)
      ? selectedChannelIDs.filter((id) => id !== chId)
      : [...selectedChannelIDs, chId];
    onSetState({ selectedChannelIDs: ids });
  };

  const removeChannel = (chId: string) => {
    onSetState({ selectedChannelIDs: selectedChannelIDs.filter((id) => id !== chId) });
  };

  return (
    <div className="space-y-6">
      <div>
        <label className="block text-xs font-semibold text-muted-foreground mb-1.5">
          {t("common.name")}
        </label>
        <Input
          nativeInput
          value={form.name}
          onChange={(e) => setForm({ name: (e.target as HTMLInputElement).value })}
          placeholder={t("agents.form.namePlaceholder")}
          className="text-sm"
        />
      </div>
      <div>
        <p className="text-xs font-semibold text-muted-foreground mb-3 flex items-center gap-1">
          <span>{t("agents.form.models")}</span>
          <span className="text-muted-foreground font-normal">
            — {t("agents.form.modelProvider")}
          </span>
        </p>
        <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-4 gap-4">
          <div className="rounded-lg border border-border p-4 space-y-4">
            <ModelComboField
              label={t("agents.form.modelDefault")}
              field="model"
              value={form.model ?? ""}
              placeholder="anthropic/claude-..."
              cachedModels={cachedModels}
              onChange={(v) => setForm({ model: v })}
            />
            <ThinkingField
              label={t("agents.form.modelThinking")}
              value={form.model_thinking ?? ""}
              onChange={(v) => setForm({ model_thinking: v })}
            />
          </div>
          <div className="rounded-lg border border-border p-4 space-y-4">
            <ModelComboField
              label={t("agents.form.modelStrong")}
              field="model_strong"
              value={form.model_strong ?? ""}
              placeholder={t("agents.form.modelFallback")}
              optional
              cachedModels={cachedModels}
              onChange={(v) => setForm({ model_strong: v })}
            />
            <ThinkingField
              label={t("agents.form.modelStrongThinking")}
              value={form.model_strong_thinking ?? ""}
              onChange={(v) => setForm({ model_strong_thinking: v })}
            />
          </div>
          <div className="rounded-lg border border-border p-4 space-y-4">
            <ModelComboField
              label={t("agents.form.modelFast")}
              field="model_fast"
              value={form.model_fast ?? ""}
              placeholder={t("agents.form.modelFallback")}
              optional
              cachedModels={cachedModels}
              onChange={(v) => setForm({ model_fast: v })}
            />
            <ThinkingField
              label={t("agents.form.modelFastThinking")}
              value={form.model_fast_thinking ?? ""}
              onChange={(v) => setForm({ model_fast_thinking: v })}
            />
          </div>
        </div>
      </div>
      {isAdmin && (
        <div>
          <label className="block text-xs font-semibold text-muted-foreground mb-1.5">
            {t("agents.form.scope")}
          </label>
          <select
            value={form.scope}
            onChange={(e) => setForm({ scope: e.target.value as "system" | "restricted" })}
            className="w-full rounded-lg border border-border bg-background px-3 py-2 text-sm outline-none focus:border-primary focus:ring-2 focus:ring-primary/20 cursor-pointer"
          >
            <option value="system">{t("agents.form.scopeSystem")}</option>
            <option value="restricted">{t("agents.form.scopeRestricted")}</option>
          </select>
        </div>
      )}
      {isAdmin && editingId && (
        <ChannelSelector
          channels={availableChannels}
          selectedIds={selectedChannelIDs}
          onToggle={toggleChannel}
          onRemove={removeChannel}
        />
      )}
      <div className="pt-4 border-t border-border">
        <label className="flex items-center gap-3 cursor-pointer">
          <Switch
            checked={form.enabled}
            onCheckedChange={(checked) => setForm({ enabled: checked })}
          />
          <span className="text-sm font-medium text-foreground">{t("agents.form.enabled")}</span>
        </label>
      </div>
    </div>
  );
}
