import { nextRowID } from "./pluginUtils";
import type { ManifestInstallDraft } from "./pluginUtils";

interface Props {
  draft: ManifestInstallDraft;
  onChange: (draft: ManifestInstallDraft) => void;
  onSave: () => void;
  onReset: () => void;
}

export function ManifestInstallEditor({ draft, onChange, onSave, onReset }: Props) {
  function update(partial: Partial<ManifestInstallDraft>) {
    onChange({ ...draft, ...partial });
  }

  function addBinary() {
    update({
      binaries: [
        ...draft.binaries,
        { id: nextRowID(), name: "", repo: "", version: "", bin_path: "", exe: "" },
      ],
    });
  }

  function removeBinary(index: number) {
    const next = [...draft.binaries];
    next.splice(index, 1);
    update({ binaries: next });
  }

  function updateBinary(index: number, field: string, value: string) {
    const next = [...draft.binaries];
    next[index] = { ...next[index], [field]: value };
    update({ binaries: next });
  }

  function addSessionEnv() {
    update({
      session_env: [
        ...draft.session_env,
        { id: nextRowID(), env_var: "", source: "static", value: "", required: false },
      ],
    });
  }

  function removeSessionEnv(index: number) {
    const next = [...draft.session_env];
    next.splice(index, 1);
    update({ session_env: next });
  }

  function updateSessionEnv(index: number, field: string, value: string | boolean) {
    const next = [...draft.session_env];
    next[index] = { ...next[index], [field]: value };
    update({ session_env: next });
  }

  return (
    <div className="px-4 pb-4 border-t border-base-300 bg-base-100/50">
      <div className="pt-4 space-y-4">
        <div className="flex items-center justify-between gap-3 flex-wrap">
          <div>
            <p className="text-sm font-medium">Tool definition</p>
            <p className="text-xs text-secondary mt-1">
              Saved to <code className="font-mono">$ANNA_HOME/plugins.yaml</code>; binaries sync after save.
            </p>
          </div>
          <div className="flex items-center gap-2">
            <button onClick={onReset} className="btn btn-ghost btn-xs">Reset</button>
            <button onClick={onSave} className="btn btn-primary btn-xs">Save definition</button>
          </div>
        </div>

        <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-4 gap-4">
          <div className="space-y-1">
            <label className="text-xs font-medium text-secondary">ID</label>
            <input
              value={draft.id}
              onChange={(e) => update({ id: e.target.value })}
              className="input input-bordered input-sm w-full font-mono"
            />
          </div>
          <div className="space-y-1">
            <label className="text-xs font-medium text-secondary">Kind</label>
            <input
              value={draft.kind}
              onChange={(e) => update({ kind: e.target.value })}
              className="input input-bordered input-sm w-full font-mono"
            />
          </div>
          <div className="space-y-1">
            <label className="text-xs font-medium text-secondary">Name</label>
            <input
              value={draft.name}
              onChange={(e) => update({ name: e.target.value })}
              className="input input-bordered input-sm w-full font-mono"
            />
          </div>
          <div className="space-y-1">
            <label className="text-xs font-medium text-secondary">Display name</label>
            <input
              value={draft.display_name}
              onChange={(e) => update({ display_name: e.target.value })}
              className="input input-bordered input-sm w-full"
            />
          </div>
          <div className="space-y-1 md:col-span-2 xl:col-span-4">
            <label className="text-xs font-medium text-secondary">Description</label>
            <input
              value={draft.description}
              onChange={(e) => update({ description: e.target.value })}
              className="input input-bordered input-sm w-full"
            />
          </div>

          {/* Binaries */}
          <div className="space-y-1 md:col-span-2 xl:col-span-4">
            <div className="flex items-center justify-between gap-2">
              <label className="text-xs font-medium text-secondary">Binaries</label>
              <button onClick={addBinary} className="btn btn-ghost btn-xs">Add binary</button>
            </div>
            <div className="space-y-2">
              {draft.binaries.map((binary, index) => (
                <div
                  key={binary.id}
                  className="grid grid-cols-1 md:grid-cols-[1fr_1.4fr_0.8fr_0.8fr_0.8fr_auto] gap-2 rounded-lg border border-base-300 p-2"
                >
                  <input
                    value={binary.name}
                    onChange={(e) => updateBinary(index, "name", e.target.value)}
                    placeholder="binary name"
                    className="input input-bordered input-sm w-full font-mono"
                  />
                  <input
                    value={binary.repo}
                    onChange={(e) => updateBinary(index, "repo", e.target.value)}
                    placeholder="owner/repo"
                    className="input input-bordered input-sm w-full font-mono"
                  />
                  <input
                    value={binary.version ?? ""}
                    onChange={(e) => updateBinary(index, "version", e.target.value)}
                    placeholder="version"
                    className="input input-bordered input-sm w-full font-mono"
                  />
                  <input
                    value={binary.bin_path ?? ""}
                    onChange={(e) => updateBinary(index, "bin_path", e.target.value)}
                    placeholder="bin path"
                    className="input input-bordered input-sm w-full font-mono"
                  />
                  <input
                    value={binary.exe ?? ""}
                    onChange={(e) => updateBinary(index, "exe", e.target.value)}
                    placeholder="exe"
                    className="input input-bordered input-sm w-full font-mono"
                  />
                  <button
                    onClick={() => removeBinary(index)}
                    className="btn btn-ghost btn-xs text-error"
                  >
                    Remove
                  </button>
                </div>
              ))}
              {draft.binaries.length === 0 && (
                <div className="rounded-lg border border-dashed border-base-300 px-3 py-3 text-xs text-secondary">
                  No binaries declared.
                </div>
              )}
            </div>
          </div>

          {/* Session env */}
          <div className="space-y-1 md:col-span-2 xl:col-span-4">
            <div className="flex items-center justify-between gap-2">
              <label className="text-xs font-medium text-secondary">Session environment</label>
              <button onClick={addSessionEnv} className="btn btn-ghost btn-xs">Add env</button>
            </div>
            <div className="space-y-2">
              {draft.session_env.map((env, index) => (
                <div
                  key={env.id}
                  className="grid grid-cols-1 md:grid-cols-[1fr_1fr_1fr_auto_auto] gap-2 rounded-lg border border-base-300 p-2 items-center"
                >
                  <input
                    value={env.env_var}
                    onChange={(e) => updateSessionEnv(index, "env_var", e.target.value)}
                    placeholder="ENV_VAR"
                    className="input input-bordered input-sm w-full font-mono"
                  />
                  <select
                    value={env.source}
                    onChange={(e) => updateSessionEnv(index, "source", e.target.value)}
                    className="select select-bordered select-sm w-full font-mono"
                  >
                    <option value="static">static</option>
                    <option value="oauth.access_token">oauth.access_token</option>
                    <option value="oauth.client_id">oauth.client_id</option>
                    <option value="oauth.brand">oauth.brand</option>
                  </select>
                  <input
                    value={env.value ?? ""}
                    onChange={(e) => updateSessionEnv(index, "value", e.target.value)}
                    placeholder="value (static only)"
                    className="input input-bordered input-sm w-full font-mono"
                  />
                  <label className="label cursor-pointer gap-2 py-0">
                    <input
                      type="checkbox"
                      checked={!!env.required}
                      onChange={(e) => updateSessionEnv(index, "required", e.target.checked)}
                      className="checkbox checkbox-xs"
                    />
                    <span className="label-text text-xs">required</span>
                  </label>
                  <button
                    onClick={() => removeSessionEnv(index)}
                    className="btn btn-ghost btn-xs text-error"
                  >
                    Remove
                  </button>
                </div>
              ))}
              {draft.session_env.length === 0 && (
                <div className="rounded-lg border border-dashed border-base-300 px-3 py-3 text-xs text-secondary">
                  No session environment variables declared.
                </div>
              )}
            </div>
          </div>

          {/* OAuth */}
          <div className="space-y-1">
            <label className="text-xs font-medium text-secondary">OAuth provider</label>
            <input
              value={draft.oauth_provider}
              onChange={(e) => update({ oauth_provider: e.target.value })}
              placeholder="github"
              className="input input-bordered input-sm w-full font-mono"
            />
          </div>
          <div className="space-y-1">
            <label className="text-xs font-medium text-secondary">Provider config field</label>
            <input
              value={draft.oauth_provider_config_field}
              onChange={(e) => update({ oauth_provider_config_field: e.target.value })}
              placeholder="brand"
              className="input input-bordered input-sm w-full font-mono"
            />
          </div>
          <div className="space-y-1 md:col-span-2">
            <label className="text-xs font-medium text-secondary">Provider choices</label>
            <input
              value={draft.oauth_provider_choices}
              onChange={(e) => update({ oauth_provider_choices: e.target.value })}
              placeholder="feishu, lark"
              className="input input-bordered input-sm w-full font-mono"
            />
          </div>
        </div>
      </div>
    </div>
  );
}
