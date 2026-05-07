interface Props {
  message: string;
  onConfirm: () => void;
  onCancel: () => void;
}

export function ConfirmDialog({ message, onConfirm, onCancel }: Props) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
      <div className="card bg-base-100 shadow-xl w-full max-w-sm" onClick={(e) => e.stopPropagation()}>
        <div className="card-body">
          <p className="text-sm">{message}</p>
          <div className="card-actions justify-end mt-4">
            <button onClick={onCancel} className="btn btn-ghost btn-sm">Cancel</button>
            <button onClick={onConfirm} className="btn btn-error btn-sm">Delete</button>
          </div>
        </div>
      </div>
    </div>
  );
}
