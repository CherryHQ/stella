import type { BuiltinItem } from "@/lib/types";
import { Dialog, DialogPopup, DialogTitle, DialogHeader, DialogDescription, DialogPanel } from "@/components/ui/dialog";

interface Props {
  templates: BuiltinItem[];
  onPick: (tmpl: BuiltinItem) => void;
  onPickBlank: () => void;
  onClose: () => void;
}

export function TemplateModal({ templates, onPick, onPickBlank, onClose }: Props) {
  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogPopup className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>Start from a template</DialogTitle>
          <DialogDescription>
            Templates pre-fill the system prompt, skills, and model. You can edit everything before saving.
          </DialogDescription>
        </DialogHeader>
        <DialogPanel>
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
            {templates.map((tmpl) => (
              <button
                key={tmpl.id}
                onClick={() => onPick(tmpl)}
                type="button"
                className="text-left border border-border hover:border-primary rounded-xl p-4 transition-colors"
              >
                <p className="font-medium text-sm">{tmpl.name}</p>
                <p className="text-xs text-muted-foreground font-mono mt-0.5">{tmpl.id}</p>
                <p className="text-xs text-muted-foreground mt-2">{tmpl.description}</p>
              </button>
            ))}
            <button
              onClick={onPickBlank}
              type="button"
              className="text-left border border-dashed border-border hover:border-primary rounded-xl p-4 transition-colors"
            >
              <p className="font-medium text-sm">Blank</p>
              <p className="text-xs text-muted-foreground mt-2">Configure everything yourself.</p>
            </button>
          </div>
        </DialogPanel>
      </DialogPopup>
    </Dialog>
  );
}
