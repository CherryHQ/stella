import { describe, expect, it } from "vitest";
import { registryPluginID } from "./useMcpMarketInstall";
import { ensureMcpBearerCredentialRef, transitionMcpAuthType } from "./mcp-credential";
import type { McpDeclaration } from "@/lib/api-client/types.gen";

describe("registryPluginID", () => {
  it("normalizes registry path punctuation without changing collision behavior", () => {
    expect(registryPluginID("com.stella/registry-add")).toBe("com-stella-registry-add");
    expect(registryPluginID("vendor..server/")).toBe("vendor-server");
  });

  it("rejects an id with no plugin-id-safe content", () => {
    expect(() => registryPluginID("///")).toThrow("registry server id cannot produce a valid name");
  });
});

describe("ensureMcpBearerCredentialRef", () => {
  it("creates distinct refs and preserves an existing ref", () => {
    const first = ensureMcpBearerCredentialRef();
    const second = ensureMcpBearerCredentialRef();
    expect(first).not.toBe(second);
    expect(first).toMatch(/^MCP_[A-F0-9]{32}_TOKEN$/);
    expect(ensureMcpBearerCredentialRef("MCP_EXISTING_TOKEN")).toBe("MCP_EXISTING_TOKEN");
  });
});

describe("transitionMcpAuthType", () => {
  const base: McpDeclaration = {
    url: "https://mcp.example.test",
    transport: "streamable_http",
    auth_type: "none",
    credential_mode: "per_user",
  };

  it("clears bearer and OAuth fields when switching to none", () => {
    expect(
      transitionMcpAuthType(
        {
          ...base,
          auth_type: "oauth",
          credential_ref: "MCP_OLD_TOKEN",
          client_id: "client",
          client_secret_ref: "secret",
          token_endpoint_auth_method: "client_secret_basic",
          scopes: ["read"],
        },
        "none",
      ),
    ).toMatchObject({ ...base, auth_type: "none" });
    expect(
      transitionMcpAuthType(
        {
          ...base,
          auth_type: "oauth",
          credential_ref: "MCP_OLD_TOKEN",
          client_id: "client",
        },
        "none",
      ),
    ).not.toHaveProperty("credential_ref");
  });

  it("preserves only an existing bearer ref for bearer auth", () => {
    const existing = transitionMcpAuthType(
      { ...base, auth_type: "bearer", credential_ref: "MCP_EXISTING_TOKEN" },
      "bearer",
    );
    expect(existing.credential_ref).toBe("MCP_EXISTING_TOKEN");

    const generated = transitionMcpAuthType(
      { ...base, auth_type: "oauth", client_id: "client" },
      "bearer",
    );
    expect(generated.credential_ref).toMatch(/^MCP_[A-F0-9]{32}_TOKEN$/);
    expect(generated).not.toHaveProperty("client_id");
  });

  it("preserves OAuth fields only when staying in OAuth", () => {
    const existing = transitionMcpAuthType(
      {
        ...base,
        auth_type: "oauth",
        credential_ref: "MCP_OAUTH_AUTH_REF",
        client_id: "client",
        client_secret_ref: "secret",
        token_endpoint_auth_method: "client_secret_post",
        scopes: ["read"],
      },
      "oauth",
    );
    expect(existing).toMatchObject({
      credential_ref: "MCP_OAUTH_AUTH_REF",
      client_id: "client",
      client_secret_ref: "secret",
      token_endpoint_auth_method: "client_secret_post",
      scopes: ["read"],
    });
    expect(
      transitionMcpAuthType({ ...base, auth_type: "bearer", credential_ref: "x" }, "oauth"),
    ).not.toHaveProperty("credential_ref");
  });
});
