import { useCallback, useRef, useState, type DragEvent } from "react";
import { CheckCircle2, FileText, LoaderCircle, Upload, X, XCircle } from "lucide-react";
import { createKnowledgeFile } from "@/lib/api-client/sdk.gen";
import type { KnowledgeFileScope } from "@/lib/api-client/types.gen";
import { apiErrorMessage } from "@/lib/api-error";
import { useI18n } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogPanel,
  DialogPopup,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";

const MAX_FILE_BYTES = 25 * 1024 * 1024;
const UPLOAD_CONCURRENCY = 3;
const ACCEPTED_EXTENSIONS = new Set([".pdf", ".docx", ".md", ".markdown", ".txt"]);

type UploadStatus = "pending" | "uploading" | "success" | "error";

interface UploadItem {
  id: string;
  file: File;
  status: UploadStatus;
  error?: string;
}

interface KnowledgeUploadDialogProps {
  open: boolean;
  scope: KnowledgeFileScope;
  agentId?: string;
  onOpenChange: (open: boolean) => void;
  onBatchComplete: (succeeded: number, failed: number) => Promise<void> | void;
}

function fileExtension(name: string) {
  const dot = name.lastIndexOf(".");
  return dot >= 0 ? name.slice(dot).toLowerCase() : "";
}

function isCompatibleMediaType(extension: string, mediaType: string) {
  // Some browsers leave File.type empty. In that case the server still
  // validates the actual bytes, so the extension check remains useful.
  if (!mediaType) return true;
  if (extension === ".pdf") return mediaType === "application/pdf";
  if (extension === ".docx") {
    return mediaType === "application/vnd.openxmlformats-officedocument.wordprocessingml.document";
  }
  if (extension === ".md" || extension === ".markdown") {
    return ["text/markdown", "text/x-markdown", "text/plain"].includes(mediaType);
  }
  return extension === ".txt" && mediaType === "text/plain";
}

export function KnowledgeUploadDialog({
  open,
  scope,
  agentId,
  onOpenChange,
  onBatchComplete,
}: KnowledgeUploadDialogProps) {
  const { t } = useI18n();
  const inputRef = useRef<HTMLInputElement>(null);
  const [items, setItems] = useState<UploadItem[]>([]);
  const [uploading, setUploading] = useState(false);
  const [dragging, setDragging] = useState(false);

  const validateFile = useCallback(
    (file: File) => {
      const extension = fileExtension(file.name);
      if (!ACCEPTED_EXTENSIONS.has(extension)) {
        return t("knowledge.upload.unsupportedExtension");
      }
      if (!isCompatibleMediaType(extension, file.type)) {
        return t("knowledge.upload.unsupportedMediaType");
      }
      if (file.size > MAX_FILE_BYTES) {
        return t("knowledge.upload.tooLarge");
      }
      return undefined;
    },
    [t],
  );

  const addFiles = useCallback(
    (files: FileList | File[]) => {
      const next = Array.from(files).map((file, index) => {
        const error = validateFile(file);
        return {
          id: `${file.name}:${file.size}:${file.lastModified}:${Date.now()}:${index}`,
          file,
          status: error ? ("error" as const) : ("pending" as const),
          error,
        };
      });
      setItems((current) => [...current, ...next]);
    },
    [validateFile],
  );

  const updateItem = useCallback((id: string, patch: Partial<UploadItem>) => {
    setItems((current) => current.map((item) => (item.id === id ? { ...item, ...patch } : item)));
  }, []);

  const handleOpenChange = (nextOpen: boolean) => {
    // Closing halfway through a batch would hide per-file outcomes while the
    // requests keep running, so the dialog stays open until all workers stop.
    if (!nextOpen && uploading) return;
    if (!nextOpen) {
      setItems([]);
      setDragging(false);
    }
    onOpenChange(nextOpen);
  };

  const handleDrop = (event: DragEvent<HTMLDivElement>) => {
    event.preventDefault();
    setDragging(false);
    if (!uploading && event.dataTransfer.files.length > 0) {
      addFiles(event.dataTransfer.files);
    }
  };

  const pending = items.filter((item) => item.status === "pending");

  const uploadPendingFiles = async () => {
    if (pending.length === 0 || uploading) return;
    setUploading(true);

    let cursor = 0;
    let succeeded = 0;
    let failed = items.filter((item) => item.status === "error").length;

    // Three small workers share one cursor. This bounds simultaneous request
    // bodies without changing the API's deliberate one-file-per-request shape.
    const worker = async () => {
      while (cursor < pending.length) {
        const item = pending[cursor++];
        updateItem(item.id, { status: "uploading", error: undefined });
        try {
          await createKnowledgeFile({
            query: { scope, agent_id: agentId },
            body: { file: item.file },
            throwOnError: true,
          });
          succeeded++;
          updateItem(item.id, { status: "success" });
        } catch (error) {
          failed++;
          updateItem(item.id, {
            status: "error",
            error: apiErrorMessage(error, t("knowledge.upload.failed")),
          });
        }
      }
    };

    try {
      await Promise.all(
        Array.from({ length: Math.min(UPLOAD_CONCURRENCY, pending.length) }, () => worker()),
      );
      await onBatchComplete(succeeded, failed);
    } finally {
      setUploading(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogPopup>
        <DialogHeader>
          <DialogTitle>{t("knowledge.upload.title")}</DialogTitle>
          <DialogDescription>{t("knowledge.upload.description")}</DialogDescription>
        </DialogHeader>
        <DialogPanel className="space-y-4">
          <div
            className={cn(
              "rounded-xl border border-dashed p-6 text-center transition-colors",
              dragging ? "border-primary bg-primary/5" : "border-border bg-muted/24",
            )}
            onDragEnter={(event) => {
              event.preventDefault();
              if (!uploading) setDragging(true);
            }}
            onDragOver={(event) => event.preventDefault()}
            onDragLeave={(event) => {
              if (!event.currentTarget.contains(event.relatedTarget as Node | null)) {
                setDragging(false);
              }
            }}
            onDrop={handleDrop}
          >
            <Upload className="mx-auto mb-3 size-7 text-muted-foreground" />
            <p className="text-sm font-medium">{t("knowledge.upload.drop")}</p>
            <p className="mt-1 text-xs text-muted-foreground">{t("knowledge.upload.formats")}</p>
            <Button
              className="mt-4"
              variant="outline"
              disabled={uploading}
              onClick={() => inputRef.current?.click()}
            >
              {t("knowledge.upload.choose")}
            </Button>
            <Input
              ref={inputRef}
              nativeInput
              type="file"
              multiple
              accept=".pdf,.docx,.md,.markdown,.txt"
              className="sr-only"
              disabled={uploading}
              onChange={(event) => {
                if (event.target.files) addFiles(event.target.files);
                // The same local file can be intentionally selected again.
                event.target.value = "";
              }}
            />
          </div>

          {items.length > 0 && (
            <ul className="max-h-64 space-y-2 overflow-y-auto" aria-live="polite">
              {items.map((item) => (
                <li
                  key={item.id}
                  className="flex items-start gap-3 rounded-lg border border-border bg-card p-3"
                >
                  <FileText className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-sm font-medium">{item.file.name}</p>
                    <p className="mt-1 text-xs text-muted-foreground">
                      {formatUploadBytes(item.file.size)}
                    </p>
                    {item.error && (
                      <p className="mt-1 break-words text-xs text-destructive">{item.error}</p>
                    )}
                  </div>
                  <UploadItemStatus status={item.status} />
                  {item.status === "pending" && !uploading && (
                    <Button
                      size="icon-xs"
                      variant="ghost"
                      aria-label={t("common.remove")}
                      onClick={() =>
                        setItems((current) =>
                          current.filter((candidate) => candidate.id !== item.id),
                        )
                      }
                    >
                      <X />
                    </Button>
                  )}
                </li>
              ))}
            </ul>
          )}
        </DialogPanel>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" disabled={uploading} />}>
            {items.some((item) => item.status === "success")
              ? t("common.done")
              : t("common.cancel")}
          </DialogClose>
          <Button
            loading={uploading}
            disabled={pending.length === 0}
            onClick={() => void uploadPendingFiles()}
          >
            {t("knowledge.upload.submit", { count: pending.length })}
          </Button>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  );
}

function UploadItemStatus({ status }: { status: UploadStatus }) {
  const { t } = useI18n();
  if (status === "uploading") {
    return (
      <span className="flex shrink-0 items-center gap-1 text-xs text-muted-foreground">
        <LoaderCircle className="size-4 animate-spin" />
        {t("knowledge.upload.uploading")}
      </span>
    );
  }
  if (status === "success") {
    return (
      <span className="flex shrink-0 items-center gap-1 text-xs text-success-foreground">
        <CheckCircle2 className="size-4" />
        {t("knowledge.upload.succeeded")}
      </span>
    );
  }
  if (status === "error") {
    return (
      <span className="flex shrink-0 items-center gap-1 text-xs text-destructive">
        <XCircle className="size-4" />
        {t("knowledge.upload.failedStatus")}
      </span>
    );
  }
  return (
    <span className="shrink-0 text-xs text-muted-foreground">{t("knowledge.upload.pending")}</span>
  );
}

function formatUploadBytes(bytes: number) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`;
}
