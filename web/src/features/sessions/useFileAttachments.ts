import { useCallback, useState } from "react";
import type { Attachment } from "./ChatComposer";

export function useFileAttachments(uploadFn: (file: File) => Promise<string>) {
  const [attachments, setAttachments] = useState<Attachment[]>([]);

  const selectFiles = useCallback(
    async (files: FileList) => {
      for (const file of Array.from(files)) {
        const placeholder: Attachment = { name: file.name, path: "", uploading: true };
        setAttachments((prev) => [...prev, placeholder]);
        try {
          const path = await uploadFn(file);
          setAttachments((prev) =>
            prev.map((a) => (a === placeholder ? { ...a, path, uploading: false } : a)),
          );
        } catch (e) {
          console.error("upload failed:", e);
          setAttachments((prev) => prev.filter((a) => a !== placeholder));
        }
      }
    },
    [uploadFn],
  );

  const removeAttachment = useCallback((idx: number) => {
    setAttachments((prev) => prev.filter((_, i) => i !== idx));
  }, []);

  const clearAttachments = useCallback(() => setAttachments([]), []);

  const buildMessageText = useCallback(
    (userInput: string): string => {
      const parts: string[] = [];
      for (const a of attachments.filter((a) => a.path)) {
        parts.push(`[file: ${a.path}]`);
      }
      if (userInput.trim()) parts.push(userInput.trim());
      return parts.join("\n");
    },
    [attachments],
  );

  return {
    attachments,
    selectFiles,
    removeAttachment,
    clearAttachments,
    buildMessageText,
    isUploading: attachments.some((a) => a.uploading),
  };
}
