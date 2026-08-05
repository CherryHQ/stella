import type { ProvisioningToken } from "@/lib/api-client/types.gen";

export const ACTIVE_PROVISIONING_TOKEN_LIMIT = 2;
export const provisioningTokenExpiryDays = [30, 90, 180, 365] as const;

export type ProvisioningTokenStatus = "active" | "revoked" | "expired";

export function provisioningTokenStatus(
  token: ProvisioningToken,
  now = Date.now(),
): ProvisioningTokenStatus {
  if (token.revoked_at) return "revoked";
  if (token.expires_at && new Date(token.expires_at).getTime() <= now) return "expired";
  return "active";
}

export function activeProvisioningTokenCount(
  tokens: ProvisioningToken[],
  now = Date.now(),
): number {
  return tokens.filter((token) => provisioningTokenStatus(token, now) === "active").length;
}

export function provisioningTokenExpiry(days: number, now = new Date()): string {
  return new Date(now.getTime() + days * 24 * 60 * 60 * 1000).toISOString();
}
