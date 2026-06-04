import type { BuiltinItem } from "@/lib/types";
import {
  Dialog,
  DialogPopup,
  DialogTitle,
  DialogHeader,
  DialogDescription,
  DialogPanel,
} from "@/components/ui/dialog";

interface Props {
  templates: BuiltinItem[];
  onPick: (tmpl: BuiltinItem) => void;
  onPickBlank: () => void;
  onClose: () => void;
}

export function TemplateModal({ templates, onPick, onPickBlank, onClose }: Props) {
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DialogPopup className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>Start from a template</DialogTitle>
          <DialogDescription>
            Templates pre-fill the system prompt, skills, and model. You can edit everything before
            saving.
          </DialogDescription>
        </DialogHeader>
        <DialogPanel>
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-3.5">
            {templates.map((tmpl) => (
              <button
                key={tmpl.id}
                onClick={() => onPick(tmpl)}
                type="button"
                className="text-left border border-border bg-card hover:border-foreground/20 hover:bg-muted/10 rounded-xl p-4 transition-all duration-120 cursor-pointer shadow-none flex flex-col justify-between"
              >
                <div>
                  <p className="font-semibold text-sm text-foreground/90">{tmpl.name}</p>
                  <p className="text-[10px] text-muted-foreground/50 font-mono mt-1 flex items-center gap-1">
                    <span className="uppercase tracking-wider">Template:</span>
                    <span>{tmpl.id}</span>
                  </p>
                </div>
                <p className="text-xs text-muted-foreground/80 leading-relaxed mt-3">
                  {tmpl.description}
                </p>
              </button>
            ))}
            <button
              onClick={onPickBlank}
              type="button"
              className="text-left border border-dashed border-border bg-transparent hover:border-foreground/20 hover:bg-muted/10 rounded-xl p-4 transition-all duration-120 cursor-pointer flex flex-col justify-between"
            >
              <div>
                <p className="font-semibold text-sm text-foreground/90">Blank</p>
                <p className="text-[10px] text-muted-foreground/50 font-mono mt-1 uppercase tracking-wider">
                  Empty Slate
                </p>
              </div>
              <p className="text-xs text-muted-foreground/80 leading-relaxed mt-3">
                Configure everything yourself from scratch.
              </p>
            </button>
          </div>
        </DialogPanel>
      </DialogPopup>
    </Dialog>
  );
}
