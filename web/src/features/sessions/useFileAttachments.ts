import { useCallback, useState } from "react";
import type { UIMessage } from "ai";
import type { Attachment } from "./ChatComposer";

export function useFileAttachments(uploadFn: (file: File) => Promise<string>) {
  const [attachments, setAttachments] = useState<Attachment[]>([]);

  const selectFiles = useCallback(
    async (files: FileList) => {
      for (const file of Array.from(files)) {
        const placeholder: Attachment = {
          name: file.name,
          path: "",
          uploading: true,
          mediaType: file.type || "application/octet-stream",
        };
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

  // Attachments travel as file parts rather than as "[file: path]" prose, so the
  // server can attach the image itself instead of hoping the model opens the
  // path. The marker still ends up in the stored message — the server writes it
  // beside the image — which is what the transcript renders attachments from.
  const buildMessageParts = useCallback(
    (userInput: string): UIMessage["parts"] => {
      const parts: UIMessage["parts"] = [];
      for (const a of attachments.filter((a) => a.path)) {
        parts.push({
          type: "file",
          url: a.path,
          mediaType: a.mediaType ?? "application/octet-stream",
          filename: a.name,
        });
      }
      if (userInput.trim()) parts.push({ type: "text", text: userInput.trim() });
      return parts;
    },
    [attachments],
  );

  // Group messages are still a single string on the wire, so that entry point
  // keeps sending the marker as prose.
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
    buildMessageParts,
    buildMessageText,
    isUploading: attachments.some((a) => a.uploading),
  };
}
