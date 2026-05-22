export function unwrapApiData<T>(value: unknown): T {
  return value && typeof value === "object" && "data" in value
    ? ((value as { data: T }).data as T)
    : (value as T);
}

export function unwrapApiList<T>(value: unknown): T[] {
  if (value == null) return [];
  const data = unwrapApiData<unknown>(value);
  return Array.isArray(data) ? (data as T[]) : [];
}

export function unwrapApiItems<T>(value: unknown): T[] {
  if (value == null) return [];
  const data = unwrapApiData<T[] | { items?: T[] }>(value);
  if (Array.isArray(data)) return data;
  return data?.items ?? [];
}
