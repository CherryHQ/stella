import type { Plugin, PluginSchemaProperty } from "@/lib/types";
import {
  pluginFieldDescription,
  pluginFieldHasEnum,
  pluginFieldInputType,
  pluginFieldIsComplex,
  pluginFieldOptionLabel,
  pluginFieldPlaceholder,
  pluginFieldRows,
  pluginFieldType,
  pluginSchemaFields,
} from "./pluginUtils";

function pluginFieldID(plugin: Plugin, fieldName: string): string {
  return plugin.id.replaceAll("/", "-") + "-" + fieldName;
}

interface Props {
  plugin: Plugin;
  schemas: Record<string, { properties?: Record<string, PluginSchemaProperty> }>;
  draft: Record<string, unknown>;
  isLoading: boolean;
  isSaving: boolean;
  onDraftChange: (fieldName: string, value: unknown) => void;
  onSave: () => void;
  onReset: () => void;
}

export function GenericConfigEditor({
  plugin,
  schemas,
  draft,
  isLoading,
  isSaving,
  onDraftChange,
  onSave,
  onReset,
}: Props) {
  const fields = pluginSchemaFields(plugin, schemas);

  return (
    <div className="px-4 pb-4 border-t border-base-300 bg-base-100/50">
      <div className="pt-4 space-y-4">
        <div className="flex items-center justify-between gap-3 flex-wrap">
          <p className="text-sm font-medium">Configuration</p>
          <div className="flex items-center gap-2">
            {isLoading && <span className="loading loading-spinner loading-xs"></span>}
            <button
              onClick={onReset}
              disabled={isLoading || isSaving}
              className="btn btn-ghost btn-xs"
            >
              Reset
            </button>
            <button
              onClick={onSave}
              disabled={isLoading || isSaving}
              className="btn btn-primary btn-xs"
            >
              Save
            </button>
          </div>
        </div>
        {isLoading ? (
          <div className="rounded-lg border border-base-300 bg-base-100 px-4 py-6 text-sm text-secondary">
            Loading…
          </div>
        ) : (
          <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
            {fields.map((field) => {
              const fid = pluginFieldID(plugin, field.name);
              const isComplex = pluginFieldIsComplex(field);
              const hasEnum = pluginFieldHasEnum(field);
              const fieldType = pluginFieldType(field.schema);
              const inputType = pluginFieldInputType(field);
              const placeholder = pluginFieldPlaceholder(field);
              const description = pluginFieldDescription(field);
              const rows = pluginFieldRows(field);
              const value = draft[field.name];

              return (
                <div
                  key={field.name}
                  className={`space-y-1${isComplex ? " lg:col-span-2" : ""}`}
                >
                  <label className="text-xs font-medium text-secondary" htmlFor={fid}>
                    {field.name}
                  </label>
                  {hasEnum ? (
                    <select
                      id={fid}
                      value={String(value ?? "")}
                      onChange={(e) => onDraftChange(field.name, e.target.value)}
                      className="select select-bordered select-sm w-full"
                    >
                      {(field.schema.enum || []).map((option) => (
                        <option key={String(option)} value={String(option)}>
                          {pluginFieldOptionLabel(option)}
                        </option>
                      ))}
                    </select>
                  ) : fieldType === "boolean" ? (
                    <label className="label cursor-pointer justify-start gap-3 rounded-lg border border-base-300 px-3 py-2">
                      <input
                        id={fid}
                        type="checkbox"
                        checked={!!value}
                        onChange={(e) => onDraftChange(field.name, e.target.checked)}
                        className="toggle toggle-primary toggle-sm"
                      />
                      <span className="label-text text-sm">{field.name}</span>
                    </label>
                  ) : isComplex ? (
                    <textarea
                      id={fid}
                      value={String(value ?? "")}
                      onChange={(e) => onDraftChange(field.name, e.target.value)}
                      rows={rows}
                      className="textarea textarea-bordered w-full font-mono text-xs"
                      placeholder={placeholder}
                    />
                  ) : (
                    <input
                      id={fid}
                      type={inputType}
                      value={String(value ?? "")}
                      onChange={(e) => onDraftChange(field.name, e.target.value)}
                      placeholder={placeholder}
                      className={`input input-bordered input-sm w-full${inputType === "password" ? " font-mono" : ""}`}
                    />
                  )}
                  {description && (
                    <p className="text-[11px] leading-relaxed text-secondary">{description}</p>
                  )}
                </div>
              );
            })}
          </div>
        )}
      </div>
    </div>
  );
}
