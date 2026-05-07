import { useState } from "react";
import type { AgentsPageState } from "../AgentsPage";

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
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");

  const filtered = search
    ? cachedModels.filter((m) => m.toLowerCase().includes(search.toLowerCase()))
    : cachedModels;

  return (
    <div className="relative">
      <label className="label">
        <span className="label-text font-mono text-sm">{label}</span>
        {optional && <span className="label-text-alt text-base-content/40">(optional)</span>}
      </label>
      <input
        type="text"
        value={value}
        onChange={(e) => {
          onChange(e.target.value);
          setSearch(e.target.value);
          setOpen(cachedModels.length > 0);
        }}
        onFocus={() => {
          setSearch("");
          setOpen(cachedModels.length > 0);
        }}
        onBlur={() => setTimeout(() => setOpen(false), 150)}
        placeholder={placeholder}
        className="input input-bordered w-full text-sm font-mono"
        autoComplete="off"
        id={`model-field-${field}`}
      />
      {open && filtered.length > 0 && (
        <div className="absolute z-20 mt-1 w-full max-h-48 overflow-y-auto bg-base-100 border border-base-300 rounded-box shadow-lg py-1">
          {filtered.map((m) => (
            <button
              key={m}
              onMouseDown={() => {
                onChange(m);
                setOpen(false);
              }}
              type="button"
              className={`w-full text-left px-3 py-1.5 text-xs font-mono hover:bg-base-200 cursor-pointer ${
                value === m ? "text-primary" : "text-base-content/70"
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
  const { form, cachedModels, isAdmin, editingId, channels, selectedChannelIDs } = state;

  const setForm = (patch: Partial<typeof form>) =>
    onSetState({ form: { ...form, ...patch } });

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
        <label className="label"><span className="label-text font-mono text-sm">Name</span></label>
        <input
          value={form.name}
          onChange={(e) => setForm({ name: e.target.value })}
          placeholder="My Agent"
          className="input input-bordered w-full text-sm"
        />
      </div>
      <div>
        <p className="text-xs font-mono font-medium text-secondary uppercase tracking-wider mb-3">
          Models <span className="normal-case tracking-normal text-base-content/50">— provider/model</span>
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
          <label className="label"><span className="label-text font-mono text-sm">Scope</span></label>
          <select
            value={form.scope}
            onChange={(e) => setForm({ scope: e.target.value as "system" | "restricted" })}
            className="select select-bordered w-full text-sm"
          >
            <option value="system">system — all users can access</option>
            <option value="restricted">restricted — only assigned users</option>
          </select>
        </div>
      )}
      {isAdmin && editingId && (
        <div>
          <label className="label"><span className="label-text font-mono text-sm">Dedicated channels</span></label>
          <p className="text-xs text-base-content/60 mb-2">Bind dedicated channel instances to this agent.</p>
          <div className="space-y-1">
            {availableDedicatedChannels.map((ch) => (
              <label
                key={ch.id}
                className="flex items-center justify-between gap-3 rounded-lg border border-base-300 px-3 py-2 cursor-pointer"
              >
                <div className="min-w-0">
                  <p className="text-sm font-mono">{ch.id}</p>
                  <p className="text-xs text-secondary">{ch.type}</p>
                </div>
                <input
                  type="checkbox"
                  checked={selectedChannelIDs.includes(ch.id)}
                  onChange={() => toggleChannel(ch.id)}
                  className="checkbox checkbox-sm checkbox-primary"
                />
              </label>
            ))}
            {availableDedicatedChannels.length === 0 && (
              <div className="text-xs text-base-content/50">No dedicated channels available.</div>
            )}
          </div>
        </div>
      )}
      <div className="pt-2 border-t border-base-300">
        <label className="flex items-center gap-3 cursor-pointer">
          <input
            type="checkbox"
            checked={form.enabled}
            onChange={(e) => setForm({ enabled: e.target.checked })}
            className="toggle toggle-primary toggle-sm"
          />
          <span className="text-sm">Enabled</span>
        </label>
      </div>
    </div>
  );
}
