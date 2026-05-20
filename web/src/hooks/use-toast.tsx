import { useCallback, useState } from "react";

interface ToastMsg {
  id: number;
  text: string;
  kind: "success" | "error";
}

let toastSeq = 0;

export function useToast(timeout = 3000) {
  const [toasts, setToasts] = useState<ToastMsg[]>([]);

  const showToast = useCallback(
    (text: string, kind: "success" | "error" = "success") => {
      const id = ++toastSeq;
      setToasts((prev) => [...prev, { id, text, kind }]);
      setTimeout(() => setToasts((prev) => prev.filter((t) => t.id !== id)), timeout);
    },
    [timeout],
  );

  return { toasts, showToast } as const;
}

export function ToastContainer({ messages }: { messages: ToastMsg[] }) {
  if (messages.length === 0) return null;
  return (
    <div className="fixed bottom-4 right-4 z-[9999] flex flex-col gap-2 pointer-events-none">
      {messages.map((m) => (
        <div
          key={m.id}
          className={`px-4 py-2.5 rounded-lg shadow-lg text-sm font-medium pointer-events-auto ${
            m.kind === "error"
              ? "bg-destructive text-destructive-foreground"
              : "bg-success text-success-foreground"
          }`}
        >
          {m.text}
        </div>
      ))}
    </div>
  );
}
