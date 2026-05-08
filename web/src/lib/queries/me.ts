import { queryOptions } from "@tanstack/react-query";

export interface MeResponse {
  id: string;
  username: string;
  role: string;
  is_admin: boolean;
}

async function fetchMe(): Promise<MeResponse> {
  const res = await fetch("/api/auth/me");
  if (!res.ok) throw new Error("Unauthenticated");
  const json = await res.json();
  return json.data;
}

export const meQueryOptions = queryOptions({
  queryKey: ["me"],
  queryFn: fetchMe,
  retry: false,
});
