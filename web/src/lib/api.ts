export async function api<T = unknown>(
  method: string,
  path: string,
  body: unknown = null,
): Promise<T> {
  const isFormData = body instanceof FormData;
  const res = await fetch(path, {
    method,
    headers: isFormData ? undefined : body ? { "Content-Type": "application/json" } : undefined,
    body: body ? (isFormData ? body : JSON.stringify(body)) : undefined,
  });
  const json = await res.json();
  if (json.error) throw new Error(json.error);
  return json.data ?? json;
}
