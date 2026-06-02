export function authErrorMessage(err: unknown, fallback: string): string {
  const apiMessage = (err as any)?.error?.message;
  if (typeof apiMessage === "string" && apiMessage) return apiMessage;
  if (err instanceof Error) return err.message;
  return fallback;
}

export function authErrorStatus(e: unknown): number | undefined {
  const err = e as any;
  return err?.error?.code ?? err?.code ?? err?.status ?? err?.response?.status;
}
