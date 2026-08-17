import { useCallback, useEffect, useRef, useState } from "react";
import type { UIMessage } from "ai";
import type { Attachment } from "./ChatComposer";
import { loadDraft, patchDraft } from "./draft-store";

function restoreAttachments(draftKey?: string): Attachment[] {
  return loadDraft(draftKey ?? null).attachments.map((a) => ({ ...a, uploading: false }));
}

/**
 * Attachments for one conversation. Pass `draftKey` (the same one the composer
 * gets) to have uploaded files survive a reload alongside the text draft.
 */
export function useFileAttachments(uploadFn: (file: File) => Promise<string>, draftKey?: string) {
  const [attachments, setAttachments] = useState<Attachment[]>(() => restoreAttachments(draftKey));

  const restoredKeyRef = useRef(draftKey);
  useEffect(() => {
    if (restoredKeyRef.current === draftKey) return;
    restoredKeyRef.current = draftKey;
    setAttachments(restoreAttachments(draftKey));
  }, [draftKey]);

  useEffect(() => {
    if (!draftKey) return;
    // Only settled uploads are restorable: an in-flight or failed one has no
    // server path to point at after a reload.
    patchDraft(draftKey, {
      attachments: attachments
        .filter((a) => a.path)
        .map((a) => ({ name: a.name, path: a.path, mediaType: a.mediaType })),
    });
  }, [attachments, draftKey]);

  const upload = useCallback(
    async (file: File, marker: Attachment) => {
      try {
        const path = await uploadFn(file);
        setAttachments((prev) =>
          prev.map((a) => (a === marker ? { ...a, path, uploading: false, error: undefined } : a)),
        );
      } catch (e) {
        console.error("upload failed:", e);
        // Keep the failed file visible: silently dropping it looks like the
        // attachment succeeded and then vanished at send time.
        setAttachments((prev) =>
          prev.map((a) =>
            a === marker
              ? { ...a, uploading: false, error: e instanceof Error ? e.message : String(e) }
              : a,
          ),
        );
      }
    },
    [uploadFn],
  );

  const selectFiles = useCallback(
    async (files: FileList) => {
      // Serial on purpose: the group uploader lazily creates the upload
      // session, and parallel uploads would each create their own.
      for (const file of Array.from(files)) {
        const placeholder: Attachment = {
          name: file.name,
          path: "",
          uploading: true,
          mediaType: file.type || "application/octet-stream",
          size: file.size,
          previewUrl: file.type.startsWith("image/") ? URL.createObjectURL(file) : undefined,
          file,
        };
        setAttachments((prev) => [...prev, placeholder]);
        await upload(file, placeholder);
      }
    },
    [upload],
  );

  const retryAttachment = useCallback(
    (idx: number) => {
      const target = attachments[idx];
      if (!target?.file) return;
      const marker: Attachment = { ...target, uploading: true, error: undefined };
      setAttachments((prev) => prev.map((a, i) => (i === idx ? marker : a)));
      void upload(target.file, marker);
    },
    [attachments, upload],
  );

  const removeAttachment = useCallback((idx: number) => {
    setAttachments((prev) => {
      const preview = prev[idx]?.previewUrl;
      if (preview) URL.revokeObjectURL(preview);
      return prev.filter((_, i) => i !== idx);
    });
  }, []);

  const clearAttachments = useCallback(() => {
    setAttachments((prev) => {
      for (const a of prev) if (a.previewUrl) URL.revokeObjectURL(a.previewUrl);
      return [];
    });
  }, []);

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
    retryAttachment,
    removeAttachment,
    clearAttachments,
    buildMessageParts,
    buildMessageText,
    isUploading: attachments.some((a) => a.uploading),
  };
}
