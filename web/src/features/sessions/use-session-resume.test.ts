import { describe, expect, it } from "vitest";
import { isRetryablePrefixError, ResumeHttpError } from "./use-session-resume";

// The resume poll only retries failures the same pinned request can recover
// from — a dropped connection or a transient 5xx. 4xx answers (401/403/…)
// are facts, not outages, and must not loop.
describe("resume failure triage", () => {
  it.each([500, 502, 503, 504, 408, 429])("retries HTTP %i", (status) => {
    expect(isRetryablePrefixError(new ResumeHttpError(status))).toBe(true);
  });

  it.each([400, 401, 403, 404, 422])("does not retry HTTP %i", (status) => {
    expect(isRetryablePrefixError(new ResumeHttpError(status))).toBe(false);
  });

  it("retries a network drop (no response status)", () => {
    expect(isRetryablePrefixError(new ResumeHttpError(undefined))).toBe(true);
    expect(isRetryablePrefixError(new TypeError("fetch failed"))).toBe(true);
  });

  it("does not retry programming errors", () => {
    expect(isRetryablePrefixError(new Error("bug"))).toBe(false);
  });
});
