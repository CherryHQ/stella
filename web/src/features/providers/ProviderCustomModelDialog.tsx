import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogPopup,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field, FieldError, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { useI18n } from "@/lib/i18n";

/**
 * Collects the one thing a custom model needs that cannot be inferred: its ID.
 * Every other field is edited inline on the model row afterwards, so this stays
 * a one-field dialog instead of a second copy of the model schema.
 */
export function ProviderCustomModelDialog({
  open,
  existingIDs,
  onOpenChange,
  onSubmit,
}: {
  open: boolean;
  existingIDs: ReadonlySet<string>;
  onOpenChange: (open: boolean) => void;
  onSubmit: (modelID: string) => void;
}) {
  const { t } = useI18n();
  const [modelID, setModelID] = useState("");
  const [error, setError] = useState("");

  useEffect(() => {
    if (open) {
      setModelID("");
      setError("");
    }
  }, [open]);

  const submit = () => {
    const trimmed = modelID.trim();
    if (!trimmed) {
      setError(t("providers.modelIdRequired"));
      return;
    }
    if (existingIDs.has(trimmed)) {
      setError(t("providers.modelExists"));
      return;
    }
    onOpenChange(false);
    onSubmit(trimmed);
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogPopup>
        <DialogHeader>
          <DialogTitle>{t("providers.addCustomModel")}</DialogTitle>
          <DialogDescription>{t("providers.customModelDesc")}</DialogDescription>
        </DialogHeader>
        <div className="px-6 pb-2">
          <Field>
            <FieldLabel>{t("providers.modelId")}</FieldLabel>
            <Input
              autoFocus
              nativeInput
              value={modelID}
              placeholder="llama3.1:8b"
              aria-invalid={error ? true : undefined}
              className="font-mono"
              onChange={(e: React.ChangeEvent<HTMLInputElement>) => {
                setModelID(e.target.value);
                setError("");
              }}
              onKeyDown={(e: React.KeyboardEvent<HTMLInputElement>) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  submit();
                }
              }}
            />
            {error && <FieldError match>{error}</FieldError>}
          </Field>
        </div>
        <DialogFooter>
          <Button onClick={() => onOpenChange(false)} variant="ghost" size="sm">
            {t("common.cancel")}
          </Button>
          <Button onClick={submit} variant="default" size="sm">
            {t("common.save")}
          </Button>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  );
}
