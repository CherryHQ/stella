import { describe, expect, it } from "vitest";
import type { ProvisioningToken } from "@/lib/api-client/types.gen";
import {
  activeProvisioningTokenCount,
  provisioningTokenExpiry,
  provisioningTokenStatus,
} from "./provisioning-helpers";

function token(overrides: Partial<ProvisioningToken> = {}): ProvisioningToken {
  return {
    id: "token-id",
    name: "Onboarding",
    last4: "1234",
    created_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

describe("provisioningTokenStatus", () => {
  const now = Date.parse("2026-01-01T00:00:00Z");

  it("gives revocation precedence over expiration", () => {
    expect(
      provisioningTokenStatus(
        token({ revoked_at: "2025-12-31T00:00:00Z", expires_at: "2025-12-31T00:00:00Z" }),
        now,
      ),
    ).toBe("revoked");
  });

  it("marks tokens expired at their expiry time", () => {
    expect(provisioningTokenStatus(token({ expires_at: "2026-01-01T00:00:00Z" }), now)).toBe(
      "expired",
    );
  });

  it("counts only active tokens toward the creation limit", () => {
    expect(
      activeProvisioningTokenCount(
        [
          token(),
          token({ id: "revoked", revoked_at: "2025-12-31T00:00:00Z" }),
          token({ id: "expired", expires_at: "2025-12-31T00:00:00Z" }),
        ],
        now,
      ),
    ).toBe(1);
  });
});

describe("provisioningTokenExpiry", () => {
  it("uses whole UTC days from the creation time", () => {
    expect(provisioningTokenExpiry(90, new Date("2026-01-01T00:00:00Z"))).toBe(
      "2026-04-01T00:00:00.000Z",
    );
  });
});
