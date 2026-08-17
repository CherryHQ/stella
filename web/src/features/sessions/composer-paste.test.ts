import { describe, expect, it } from "vitest";
import { PASTE_AS_FILE_CHARS, pastedFileName, shouldPasteAsFile } from "./composer-paste";

describe("shouldPasteAsFile", () => {
  it("leaves ordinary pastes alone and files only the dumps", () => {
    expect(shouldPasteAsFile("a short note")).toBe(false);
    expect(shouldPasteAsFile("x".repeat(PASTE_AS_FILE_CHARS))).toBe(false);
    expect(shouldPasteAsFile("x".repeat(PASTE_AS_FILE_CHARS + 1))).toBe(true);
  });
});

describe("pastedFileName", () => {
  it("is a sortable UTC stamp with a .txt extension", () => {
    expect(pastedFileName(new Date("2026-08-17T11:22:33.456Z"))).toBe("pasted-20260817T112233.txt");
  });
});
