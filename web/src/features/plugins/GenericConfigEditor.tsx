import type { JsonObject, JsonValue, Plugin, PluginSchemaProperty } from "@/lib/types";
import { targetValue } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/lib/i18n";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Switch } from "@/components/ui/switch";
import { Spinner } from "@/components/ui/spinner";
import {
  pluginFieldDescription,
  pluginFieldHasEnum,
  pluginFieldInputType,
  pluginFieldIsComplex,
  pluginFieldOptionLabel,
  pluginFieldPlaceholder,
  pluginFieldRows,
  pluginFieldText,
  pluginFieldType,
  pluginSchemaFields,
} from "./pluginUtils";

function pluginFieldID(plugin: Plugin, fieldName: string): string {
  return plugin.id.replaceAll("/", "-") + "-" + fieldName;
}

interface Props {
  plugin: Plugin;
  schemas: Record<string, { properties?: Record<string, PluginSchemaProperty> }>;
  draft: JsonObject;
  isLoading: boolean;
  isSaving: boolean;
  onDraftChange: (fieldName: string, value: JsonValue) => void;
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
  const { t } = useI18n();
  const fields = pluginSchemaFields(plugin, schemas);

  return (
    <div className="px-4 pb-4 border-t border-border bg-muted">
      <div className="pt-4 space-y-4">
        <div className="flex items-center justify-between gap-3 flex-wrap">
          <p className="text-sm font-medium">{t("plugins.configuration")}</p>
          <div className="flex items-center gap-2">
            {isLoading && <Spinner className="size-4" />}
            <Button onClick={onReset} disabled={isLoading || isSaving} variant="ghost" size="xs">
              {t("common.reset")}
            </Button>
            <Button onClick={onSave} disabled={isLoading || isSaving} variant="default" size="xs">
              {t("common.save")}
            </Button>
          </div>
        </div>
        {isLoading ? (
          <div className="rounded-lg border border-border bg-card px-4 py-6 text-sm text-muted-foreground">
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
                <div key={field.name} className={`space-y-1${isComplex ? " lg:col-span-2" : ""}`}>
                  <label className="text-xs font-medium text-muted-foreground" htmlFor={fid}>
                    {field.name}
                  </label>
                  {hasEnum ? (
                    <select
                      id={fid}
                      value={pluginFieldText(value)}
                      onChange={(e) => onDraftChange(field.name, e.target.value)}
                      className="h-9 w-full rounded-lg border border-input bg-background px-3 text-sm outline-none sm:h-8"
                    >
                      {(field.schema.enum || []).map((option) => (
                        <option
                          key={pluginFieldOptionLabel(option)}
                          value={pluginFieldOptionLabel(option)}
                        >
                          {pluginFieldOptionLabel(option)}
                        </option>
                      ))}
                    </select>
                  ) : fieldType === "boolean" ? (
                    <div className="flex items-center gap-3 rounded-lg border border-border px-3 py-2">
                      <Switch
                        id={fid}
                        checked={!!value}
                        onCheckedChange={(checked) => onDraftChange(field.name, checked)}
                      />
                      <span className="text-sm">{field.name}</span>
                    </div>
                  ) : isComplex ? (
                    <Textarea
                      id={fid}
                      value={pluginFieldText(value)}
                      onChange={(e) => onDraftChange(field.name, e.target.value)}
                      rows={rows}
                      className="font-mono text-xs"
                      placeholder={placeholder}
                    />
                  ) : (
                    <Input
                      id={fid}
                      nativeInput
                      type={inputType}
                      value={pluginFieldText(value)}
                      onChange={(e) => onDraftChange(field.name, targetValue(e))}
                      placeholder={placeholder}
                      className={inputType === "password" ? "font-mono" : undefined}
                      size="sm"
                    />
                  )}
                  {description && (
                    <p className="text-xs leading-relaxed text-muted-foreground">{description}</p>
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
