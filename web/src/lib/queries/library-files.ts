import { infiniteQueryOptions } from "@tanstack/react-query";
import { listLibraryFiles } from "@/lib/api-client/sdk.gen";
import type { LibraryFileList, LibraryFileScope } from "@/lib/api-client/types.gen";

export const KNOWLEDGE_FILE_PAGE_SIZE = 50;

export interface LibraryFileFilters {
  scope: LibraryFileScope;
  agentID?: string;
  query?: string;
}

export function libraryFilesInfiniteQueryOptions(filters: LibraryFileFilters) {
  const query = filters.query?.trim() ?? "";
  const needsAgent = filters.scope === "user_agent" || filters.scope === "system_agent";
  return infiniteQueryOptions({
    queryKey: [
      "library-files",
      filters.scope,
      filters.agentID ?? "",
      query,
      KNOWLEDGE_FILE_PAGE_SIZE,
    ],
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) => {
      const { data } = await listLibraryFiles({
        query: {
          scope: filters.scope,
          ...(filters.agentID ? { agent_id: filters.agentID } : {}),
          ...(query ? { q: query } : {}),
          page_size: KNOWLEDGE_FILE_PAGE_SIZE,
          ...(pageParam ? { page_token: pageParam } : {}),
        },
        throwOnError: true,
      });
      return data;
    },
    getNextPageParam: (lastPage) => lastPage.next_page_token ?? undefined,
    enabled: !needsAgent || !!filters.agentID,
  });
}

export function flattenLibraryFilePages(pages?: LibraryFileList[]) {
  return pages?.flatMap((page) => page.library_files) ?? [];
}
