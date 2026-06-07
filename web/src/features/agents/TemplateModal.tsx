import type { BuiltinItem } from "@/lib/types";
import {
  Dialog,
  DialogPopup,
  DialogTitle,
  DialogHeader,
  DialogDescription,
  DialogPanel,
} from "@/components/ui/dialog";
import { Card } from "@/components/ui/card";
import { useI18n } from "@/lib/i18n";

interface Props {
  templates: BuiltinItem[];
  onPick: (tmpl: BuiltinItem) => void;
  onPickBlank: () => void;
  onClose: () => void;
}

export function TemplateModal({ templates, onPick, onPickBlank, onClose }: Props) {
  const { t } = useI18n();
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DialogPopup className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t("agents.template.startFrom")}</DialogTitle>
          <DialogDescription>{t("agents.template.templateDesc")}</DialogDescription>
        </DialogHeader>
        <DialogPanel>
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-3.5">
            {templates.map((tmpl) => (
              <Card
                key={tmpl.id}
                render={<button type="button" />}
                onClick={() => onPick(tmpl)}
                className="p-4 cursor-pointer hover:border-foreground/20 text-left"
              >
                <div>
                  <p className="font-semibold text-sm text-foreground">{tmpl.name}</p>
                  <p className="text-[10px] text-muted-foreground font-mono mt-1 flex items-center gap-1">
                    <span>{t("agents.template.templateLabel")}</span>
                    <span>{tmpl.id}</span>
                  </p>
                </div>
                <p className="text-xs text-muted-foreground leading-relaxed mt-3">
                  {tmpl.description}
                </p>
              </Card>
            ))}
            <Card
              render={<button type="button" />}
              onClick={onPickBlank}
              className="p-4 cursor-pointer hover:border-foreground/20 text-left border-dashed"
            >
              <div>
                <p className="font-semibold text-sm text-foreground">
                  {t("agents.template.blank")}
                </p>
                <p className="text-[10px] text-muted-foreground font-mono mt-1">
                  {t("agents.template.blankSlate")}
                </p>
              </div>
              <p className="text-xs text-muted-foreground leading-relaxed mt-3">
                {t("agents.template.blankDesc")}
              </p>
            </Card>
          </div>
        </DialogPanel>
      </DialogPopup>
    </Dialog>
  );
}
