export function unwrapApiData<T>(value: unknown): T {
  return value && typeof value === "object" && "data" in value
    ? ((value as { data: T }).data as T)
    : (value as T);
}

export function unwrapApiList<T>(value: unknown): T[] {
  if (value == null) return [];
  return unwrapApiData(value);
}

export function unwrapApiItems<T>(value: unknown): T[] {
  if (value == null) return [];
  const data = unwrapApiData<T[] | { items?: T[] }>(value);
  return Array.isArray(data) ? data : (data.items ?? []);
}
