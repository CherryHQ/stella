import { describe, expect, it } from "vitest";
import {
  applyTriggerSelection,
  filterTriggerItems,
  findTriggerFragment,
  skillTrigger,
  type ComposerTrigger,
} from "./composer-triggers";

const skills = skillTrigger([
  { name: "compact", description: "Compact session memory" },
  { name: "review", description: "Review the diff" },
]);

const mentions: ComposerTrigger = {
  char: "@",
  items: [
    { key: "scout", label: "@scout", description: "Scout" },
    { key: "mason", label: "@mason", description: "Mason" },
  ],
  replace: (item) => `${item.label} `,
};

const triggers = [skills, mentions];

describe("findTriggerFragment", () => {
  it("opens on a char that starts a word and captures the query", () => {
    expect(findTriggerFragment("/comp", 5, triggers)).toEqual({ char: "/", query: "comp", at: 0 });
    expect(findTriggerFragment("hey @sc", 7, triggers)).toEqual({ char: "@", query: "sc", at: 4 });
  });

  it("stays closed mid-word so paths and emails do not trigger it", () => {
    expect(findTriggerFragment("src/features", 12, triggers)).toBeNull();
    expect(findTriggerFragment("me@example.com", 14, triggers)).toBeNull();
  });

  it("matches against the caret, not the end of the value", () => {
    expect(findTriggerFragment("/rev tail", 4, triggers)).toEqual({
      char: "/",
      query: "rev",
      at: 0,
    });
    expect(findTriggerFragment("/rev tail", 9, triggers)).toBeNull();
  });

  it("picks the trigger closest to the caret when both are in range", () => {
    expect(findTriggerFragment("/compact @sc", 12, triggers)).toEqual({
      char: "@",
      query: "sc",
      at: 9,
    });
  });
});

describe("filterTriggerItems", () => {
  it("matches label or description, case-insensitively", () => {
    expect(filterTriggerItems(skills, "MEMORY", new Set()).map((i) => i.key)).toEqual(["compact"]);
    expect(filterTriggerItems(skills, "", new Set()).map((i) => i.key)).toEqual([
      "compact",
      "review",
    ]);
  });

  it("hides already-pinned items for chip triggers but not for inline ones", () => {
    expect(filterTriggerItems(skills, "", new Set(["compact"])).map((i) => i.key)).toEqual([
      "review",
    ]);
    // Mentions live in the text and stay repeatable.
    expect(filterTriggerItems(mentions, "", new Set(["scout"])).map((i) => i.key)).toEqual([
      "scout",
      "mason",
    ]);
  });
});

describe("applyTriggerSelection", () => {
  it("drops the fragment and leaves the caret in place for chip triggers", () => {
    const value = "/comp rest";
    const fragment = findTriggerFragment(value, 5, triggers)!;
    expect(applyTriggerSelection(value, 5, fragment, "")).toEqual({ value: " rest", caret: 0 });
  });

  it("keeps trailing text and puts the caret after an inline replacement", () => {
    const value = "ping @sc please";
    const fragment = findTriggerFragment(value, 8, triggers)!;
    expect(applyTriggerSelection(value, 8, fragment, "@scout ")).toEqual({
      value: "ping @scout  please",
      caret: 12,
    });
  });

  it("replaces the fragment at the caret, not the last matching char", () => {
    const value = "@mason and @sc";
    const fragment = findTriggerFragment(value, 14, triggers)!;
    expect(applyTriggerSelection(value, 14, fragment, "@scout ")).toEqual({
      value: "@mason and @scout ",
      caret: 18,
    });
  });
});
