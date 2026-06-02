import { useRef, useState } from "react";
import type { Channel } from "@/lib/api-client";
import type { AgentsPageState, ModelOption } from "../AgentsPage";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { useI18n } from "@/lib/i18n";

interface Props {
  state: AgentsPageState;
  onSetState: (patch: Partial<AgentsPageState>) => void;
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
      <div className="flex items-center justify-between mb-1">
        <label className="text-sm font-mono" htmlFor={`model-field-${field}`}>
          {label}
        </label>
        {optional && (
          <span className="text-xs text-muted-foreground">({t("common.optional")})</span>
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
        <div className="absolute z-20 mt-1 w-full max-h-48 overflow-y-auto bg-popover border border-border rounded-xl shadow-lg py-1">
          {filtered.map((m) => (
            <button
              key={m.value}
              onMouseDown={() => {
                onChange(m.value);
                setOpen(false);
              }}
              type="button"
              className={`w-full text-left px-3 py-1.5 text-xs font-mono hover:bg-muted cursor-pointer ${
                value === m.value ? "text-primary" : "text-muted-foreground"
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

function channelDisplayName(ch: Channel): string {
  return ch.name || ch.type;
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
  const [open, setOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);
  const unselected = channels.filter((ch) => !selectedIds.includes(ch.id));
  const selected = channels.filter((ch) => selectedIds.includes(ch.id));

  return (
    <div>
      <label className="block text-sm font-mono mb-1">Dedicated channels</label>
      <p className="text-xs text-muted-foreground mb-2">Bind channel instances to this agent.</p>
      <div ref={containerRef} className="relative">
        <div
          className="min-h-9 w-full rounded-lg border border-input bg-background px-3 py-1.5 text-sm cursor-pointer flex flex-wrap gap-1.5 items-center"
          onClick={() => setOpen((v) => !v)}
          onKeyDown={(e) => e.key === "Enter" && setOpen((v) => !v)}
          role="combobox"
          aria-expanded={open}
          tabIndex={0}
        >
          {selected.length === 0 && <span className="text-muted-foreground">Select channels…</span>}
          {selected.map((ch) => (
            <span
              key={ch.id}
              className="inline-flex items-center gap-1 rounded-md bg-muted px-2 py-0.5 text-xs font-mono"
            >
              {channelDisplayName(ch)}
              <button
                type="button"
                onClick={(e) => {
                  e.stopPropagation();
                  onRemove(ch.id);
                }}
                className="text-muted-foreground hover:text-foreground ml-0.5"
              >
                ×
              </button>
            </span>
          ))}
        </div>
        {open && (
          <>
            <div className="fixed inset-0 z-10" onClick={() => setOpen(false)} />
            <div className="absolute z-20 mt-1 w-full max-h-48 overflow-y-auto bg-popover border border-border rounded-xl shadow-lg py-1">
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
                  className="w-full text-left px-3 py-1.5 hover:bg-muted cursor-pointer flex items-center justify-between"
                >
                  <div className="min-w-0">
                    <p className="text-sm truncate">{channelDisplayName(ch)}</p>
                    <p className="text-[11px] text-muted-foreground truncate">{ch.type}</p>
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
    <div className="space-y-4">
      <div>
        <label className="block text-sm font-mono mb-1">{t("common.name")}</label>
        <Input
          nativeInput
          value={form.name}
          onChange={(e) => setForm({ name: (e.target as HTMLInputElement).value })}
          placeholder="My Agent"
          className="text-sm"
        />
      </div>
      <div>
        <p className="text-xs font-mono font-medium text-muted-foreground uppercase tracking-wider mb-3">
          Models{" "}
          <span className="normal-case tracking-normal text-muted-foreground/50">
            — provider/model
          </span>
        </p>
        <div className="space-y-3">
          <ModelComboField
            label="Default"
            field="model"
            value={form.model ?? ""}
            placeholder="anthropic/claude-..."
            cachedModels={cachedModels}
            onChange={(v) => setForm({ model: v })}
          />
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
            <ModelComboField
              label="Strong"
              field="model_strong"
              value={form.model_strong ?? ""}
              placeholder="Falls back to default"
              optional
              cachedModels={cachedModels}
              onChange={(v) => setForm({ model_strong: v })}
            />
            <ModelComboField
              label="Fast"
              field="model_fast"
              value={form.model_fast ?? ""}
              placeholder="Falls back to default"
              optional
              cachedModels={cachedModels}
              onChange={(v) => setForm({ model_fast: v })}
            />
          </div>
        </div>
      </div>
      {isAdmin && (
        <div>
          <label className="block text-sm font-mono mb-1">Scope</label>
          <select
            value={form.scope}
            onChange={(e) => setForm({ scope: e.target.value as "system" | "restricted" })}
            className="w-full rounded-lg border border-input bg-background px-3 py-2 text-sm outline-none focus:ring-2 focus:ring-ring"
          >
            <option value="system">system — all users can access</option>
            <option value="restricted">restricted — only assigned users</option>
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
      <div className="pt-2 border-t border-border">
        <label className="flex items-center gap-3 cursor-pointer">
          <Switch
            checked={form.enabled}
            onCheckedChange={(checked) => setForm({ enabled: checked })}
          />
          <span className="text-sm">Enabled</span>
        </label>
      </div>
    </div>
  );
}
