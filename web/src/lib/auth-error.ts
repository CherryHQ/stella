import { isString } from "./route-search";

export function authErrorMessage<TError>(err: TError, fallback: string): string {
  // SAFETY: the API error body may carry any shape; we only read the optional message path.
  const apiMessage = (err as any)?.error?.message;
  if (isString(apiMessage) && apiMessage) return apiMessage;
  if (err instanceof Error) return err.message;
  return fallback;
}

export function authErrorStatus<TError>(e: TError): number | undefined {
  // SAFETY: the thrown value may not be an Error; read status defensively.
  const err = e as any;
  return err?.error?.code ?? err?.code ?? err?.status ?? err?.response?.status;
}
