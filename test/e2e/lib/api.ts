// Minimal JSON + SSE client for the Stella HTTP API, authenticated with a PAT.

export interface ApiResponse<T = unknown> {
  status: number;
  headers: Headers;
  body: T;
}

export class ApiClient {
  constructor(readonly baseURL: string, readonly token: string) {}

  async request<T = unknown>(method: string, path: string, body?: unknown, headers: Record<string, string> = {}): Promise<ApiResponse<T>> {
    const res = await fetch(this.baseURL + path, {
      method,
      headers: { Authorization: `Bearer ${this.token}`, ...(body !== undefined ? { "Content-Type": "application/json" } : {}), ...headers },
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
    const text = await res.text();
    let parsed: unknown = text;
    if (text) {
      try {
        parsed = JSON.parse(text);
      } catch {
        parsed = text;
      }
    }
    return { status: res.status, headers: res.headers, body: parsed as T };
  }

  async upload<T = unknown>(path: string, filename: string, data: Uint8Array, contentType: string): Promise<ApiResponse<T>> {
    const form = new FormData();
    form.append("file", new Blob([Buffer.from(data)], { type: contentType }), filename);
    const res = await fetch(this.baseURL + path, { method: "POST", headers: { Authorization: `Bearer ${this.token}` }, body: form });
    const text = await res.text();
    let parsed: unknown = text;
    if (text) {
      try {
        parsed = JSON.parse(text);
      } catch {
        parsed = text;
      }
    }
    return { status: res.status, headers: res.headers, body: parsed as T };
  }

  get<T = unknown>(path: string, headers?: Record<string, string>) {
    return this.request<T>("GET", path, undefined, headers);
  }
  post<T = unknown>(path: string, body?: unknown, headers?: Record<string, string>) {
    return this.request<T>("POST", path, body, headers);
  }
  put<T = unknown>(path: string, body?: unknown, headers?: Record<string, string>) {
    return this.request<T>("PUT", path, body, headers);
  }
  patch<T = unknown>(path: string, body?: unknown, headers?: Record<string, string>) {
    return this.request<T>("PATCH", path, body, headers);
  }
  delete<T = unknown>(path: string, headers?: Record<string, string>) {
    return this.request<T>("DELETE", path, undefined, headers);
  }

  async stream(path: string, body: unknown): Promise<{ status: number; events: Record<string, unknown>[]; }> {
    const res = await fetch(this.baseURL + path, {
      method: "POST",
      headers: { Authorization: `Bearer ${this.token}`, "Content-Type": "application/json", Accept: "text/event-stream" },
      body: JSON.stringify(body),
    });
    const events: Record<string, unknown>[] = [];
    if (!res.ok || !res.body) {
      events.push({ type: "http-error", status: res.status, body: await res.text() });
      return { status: res.status, events };
    }
    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      let index: number;
      while ((index = buffer.indexOf("\n\n")) >= 0) {
        const frame = buffer.slice(0, index);
        buffer = buffer.slice(index + 2);
        for (const line of frame.split("\n")) {
          if (!line.startsWith("data: ")) continue;
          const data = line.slice(6);
          if (data === "[DONE]") return { status: res.status, events };
          try {
            events.push(JSON.parse(data) as Record<string, unknown>);
          } catch {
            events.push({ type: "unparsed", raw: data });
          }
        }
      }
    }
    return { status: res.status, events };
  }
}

export function expectStatus<T>(res: ApiResponse<T>, want: number, what: string): T {
  if (res.status !== want) throw new Error(`${what}: want HTTP ${want}, got ${res.status}: ${JSON.stringify(res.body)}`);
  return res.body;
}
