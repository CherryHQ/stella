import { useState } from "react";
import type { AgentsPageState } from "../AgentsPage";
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
  cachedModels: string[];
  onChange: (val: string) => void;
}) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");

  const filtered = search
    ? cachedModels.filter((m) => m.toLowerCase().includes(search.toLowerCase()))
    : cachedModels;

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
        value={value}
        onChange={(e) => {
          onChange((e.target as HTMLInputElement).value);
          setSearch((e.target as HTMLInputElement).value);
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
              key={m}
              onMouseDown={() => {
                onChange(m);
                setOpen(false);
              }}
              type="button"
              className={`w-full text-left px-3 py-1.5 text-xs font-mono hover:bg-muted cursor-pointer ${
                value === m ? "text-primary" : "text-muted-foreground"
              }`}
            >
              {m}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

export function ConfigTab({ state, onSetState }: Props) {
  const { t } = useI18n();
  const { form, cachedModels, isAdmin, editingId, channels, selectedChannelIDs } = state;

  const setForm = (patch: Partial<typeof form>) => onSetState({ form: { ...form, ...patch } });

  const availableDedicatedChannels = channels.filter(
    (ch) => ch.id !== ch.type && ch.enabled && (!ch.agent_id || ch.agent_id === editingId),
  );

  const toggleChannel = (chId: string) => {
    const ids = selectedChannelIDs.includes(chId)
      ? selectedChannelIDs.filter((id) => id !== chId)
      : [...selectedChannelIDs, chId];
    onSetState({ selectedChannelIDs: ids });
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
            value={form.model}
            placeholder="anthropic/claude-..."
            cachedModels={cachedModels}
            onChange={(v) => setForm({ model: v })}
          />
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
            <ModelComboField
              label="Strong"
              field="model_strong"
              value={form.model_strong}
              placeholder="Falls back to default"
              optional
              cachedModels={cachedModels}
              onChange={(v) => setForm({ model_strong: v })}
            />
            <ModelComboField
              label="Fast"
              field="model_fast"
              value={form.model_fast}
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
        <div>
          <label className="block text-sm font-mono mb-1">Dedicated channels</label>
          <p className="text-xs text-muted-foreground mb-2">
            Bind dedicated channel instances to this agent.
          </p>
          <div className="space-y-1">
            {availableDedicatedChannels.map((ch) => (
              <label
                key={ch.id}
                className="flex items-center justify-between gap-3 rounded-lg border border-border px-3 py-2 cursor-pointer"
              >
                <div className="min-w-0">
                  <p className="text-sm font-mono">{ch.id}</p>
                  <p className="text-xs text-muted-foreground">{ch.type}</p>
                </div>
                <Switch
                  checked={selectedChannelIDs.includes(ch.id)}
                  onCheckedChange={() => toggleChannel(ch.id)}
                />
              </label>
            ))}
            {availableDedicatedChannels.length === 0 && (
              <div className="text-xs text-muted-foreground">No dedicated channels available.</div>
            )}
          </div>
        </div>
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
