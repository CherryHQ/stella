import { describe, expect, it } from "vitest";
import { isBinary, isNonTextFile, isPdf } from "./file-kind";

describe("isBinary", () => {
  it("covers Office containers so they are never fetched as text", () => {
    for (const ext of ["docx", "xlsx", "pptx", "doc", "xls", "ppt"]) {
      expect(isBinary(`/user/assets/report.${ext}`)).toBe(true);
    }
  });

  it("leaves text-ish and unknown extensions to the text reader", () => {
    for (const path of ["notes.md", "data.csv", "server.log", "Makefile", "config"]) {
      expect(isBinary(path)).toBe(false);
    }
  });
});

describe("isNonTextFile", () => {
  it("still classifies PDFs apart from text, so callers can render them natively", () => {
    expect(isPdf("a.PDF")).toBe(true);
    expect(isNonTextFile("a.pdf")).toBe(true);
  });
});
