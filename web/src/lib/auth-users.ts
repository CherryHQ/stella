import { listAuthUsers } from "@/lib/api-client";
import type { User } from "@/lib/types";

// fetchAllAuthUsers pages through the admin user list until exhausted.
// The endpoint is cursor-paginated; admin views need the full set.
export async function fetchAllAuthUsers(): Promise<User[]> {
  const all: User[] = [];
  let pageToken: string | undefined;
  do {
    const { data } = await listAuthUsers({
      query: { page_size: 500, page_token: pageToken },
      throwOnError: true,
    });
    // SAFETY: listAuthUsers returns user items under data.users.
    all.push(...((data?.users ?? []) as User[]));
    pageToken = data?.next_page_token ?? undefined;
  } while (pageToken);
  return all;
}
