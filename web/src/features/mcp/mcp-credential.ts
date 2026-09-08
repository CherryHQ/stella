import type { McpDeclaration } from "@/lib/api-client/types.gen";

export function ensureMcpBearerCredentialRef(existing?: string): string {
  return (
    existing ?? `MCP_${globalThis.crypto.randomUUID().replaceAll("-", "").toUpperCase()}_TOKEN`
  );
}

const OAUTH_FIELDS = [
  "client_id",
  "client_secret_ref",
  "token_endpoint_auth_method",
  "scopes",
] as const;

/**
 * Change MCP authentication without carrying credentials across auth modes.
 *
 * A bearer reference is deliberately scoped to bearer auth. OAuth fields are
 * retained only while editing an existing OAuth declaration, so switching
 * away from OAuth cannot leak stale client configuration into another mode.
 */
export function transitionMcpAuthType(
  source: McpDeclaration,
  authType: McpDeclaration["auth_type"],
): McpDeclaration {
  const next = { ...source, auth_type: authType };

  for (const field of OAUTH_FIELDS) delete next[field];
  delete next.credential_ref;

  if (authType === "oauth" && source.auth_type === "oauth") {
    if (source.credential_ref !== undefined) next.credential_ref = source.credential_ref;
    if (source.client_id !== undefined) next.client_id = source.client_id;
    if (source.client_secret_ref !== undefined) next.client_secret_ref = source.client_secret_ref;
    if (source.token_endpoint_auth_method !== undefined)
      next.token_endpoint_auth_method = source.token_endpoint_auth_method;
    if (source.scopes !== undefined) next.scopes = source.scopes;
  }

  if (authType === "bearer") {
    next.credential_ref =
      source.auth_type === "bearer"
        ? ensureMcpBearerCredentialRef(source.credential_ref)
        : ensureMcpBearerCredentialRef();
  }

  return next;
}
