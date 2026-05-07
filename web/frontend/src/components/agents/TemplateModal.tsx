import type { BuiltinItem } from "@/lib/types";

interface Props {
  templates: BuiltinItem[];
  onPick: (tmpl: BuiltinItem) => void;
  onPickBlank: () => void;
  onClose: () => void;
}

export function TemplateModal({ templates, onPick, onPickBlank, onClose }: Props) {
  return (
    <div className="modal modal-open">
      <div className="modal-box w-11/12 max-w-2xl">
        <div className="flex items-center justify-between mb-4">
          <h3 className="font-bold text-lg">Start from a template</h3>
          <button onClick={onClose} className="btn btn-ghost btn-sm btn-circle">✕</button>
        </div>
        <p className="text-sm text-base-content/60 mb-4">
          Templates pre-fill the system prompt, skills, and model. You can edit everything before saving.
        </p>
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
          {templates.map((tmpl) => (
            <button
              key={tmpl.id}
              onClick={() => onPick(tmpl)}
              type="button"
              className="text-left border border-base-300 hover:border-primary rounded-box p-4 transition-colors"
            >
              <p className="font-medium text-sm">{tmpl.name}</p>
              <p className="text-xs text-secondary font-mono mt-0.5">{tmpl.id}</p>
              <p className="text-xs text-base-content/70 mt-2">{tmpl.description}</p>
            </button>
          ))}
          <button
            onClick={onPickBlank}
            type="button"
            className="text-left border border-dashed border-base-300 hover:border-primary rounded-box p-4 transition-colors"
          >
            <p className="font-medium text-sm">Blank</p>
            <p className="text-xs text-base-content/70 mt-2">Configure everything yourself.</p>
          </button>
        </div>
      </div>
      <div className="modal-backdrop" onClick={onClose}></div>
    </div>
  );
}
