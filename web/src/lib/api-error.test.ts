import { describe, expect, it } from "vitest";
import { apiErrorStatus } from "./api-error";

describe("apiErrorStatus", () => {
  it("reads the generated client's parsed structured error body", () => {
    expect(apiErrorStatus({ error: { code: 409, message: "skill changed" } })).toBe(409);
  });

  it("does not mistake unstructured errors for API responses", () => {
    expect(apiErrorStatus(new Error("409"))).toBeUndefined();
  });
});
