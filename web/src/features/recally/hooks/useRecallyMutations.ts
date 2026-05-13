import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useI18n } from "@/lib/i18n";
import {
  listArticlesQueryKey,
  getArticleQueryKey,
  getDigestQueryKey,
  updateArticleMutation,
  deleteArticleMutation,
} from "@/lib/api-client/@tanstack/react-query.gen";

export function useRecallyMutations(
  selectedId: string | null,
  setSelectedId: (id: string | null) => void,
) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const [toast, setToast] = useState<{ message: string; type: "success" | "error" } | null>(null);

  const showToast = (message: string, type: "success" | "error" = "success") => {
    setToast({ message, type });
    setTimeout(() => setToast(null), 3000);
  };

  const updateArticleMut = useMutation({
    ...updateArticleMutation(),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: listArticlesQueryKey() });
      if (selectedId) {
        void queryClient.invalidateQueries({
          queryKey: getArticleQueryKey({
            path: { id: selectedId },
            query: { include: "content" },
          }),
        });
      }
      void queryClient.invalidateQueries({ queryKey: getDigestQueryKey() });
      showToast(t("recally.article.updated"));
    },
    onError: () => {
      showToast(t("recally.article.updateFailed"), "error");
    },
  });

  const deleteArticleMut = useMutation({
    ...deleteArticleMutation(),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: listArticlesQueryKey() });
      void queryClient.invalidateQueries({ queryKey: getDigestQueryKey() });
      setSelectedId(null);
      showToast(t("recally.article.deleted"));
    },
    onError: () => {
      showToast(t("recally.article.deleteFailed"), "error");
    },
  });

  return { t, toast, showToast, updateArticleMut, deleteArticleMut };
}
