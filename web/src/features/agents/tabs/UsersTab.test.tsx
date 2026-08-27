import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import type { User } from "@/lib/types";
import { UsersTab, type UsersTabState } from "./UsersTab";

vi.hoisted(() => {
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: { getItem: () => "en", setItem: () => undefined },
  });
});

// SAFETY: fixed User-shaped test fixtures.
const assignedUsers = [{ id: "u2", email: "colleague@example.com", name: "Colleague" }] as User[];
// SAFETY: fixed User-shaped test fixtures.
const availableUsers = [{ id: "u3", email: "stranger@example.com", name: "Stranger" }] as User[];

function render(isAdmin: boolean, scope: "restricted" | "system") {
  const state: UsersTabState = {
    isAdmin,
    assignedUsers,
    addUserId: "",
    form: { scope },
  };
  return renderToStaticMarkup(
    <UsersTab
      state={state}
      availableUsers={availableUsers}
      onSetState={vi.fn()}
      onAddUser={vi.fn()}
      onRemoveUser={vi.fn()}
    />,
  );
}

describe("UsersTab", () => {
  it("never names another user to an ordinary owner", () => {
    const html = render(false, "restricted");
    // The reach control is the owner's whole surface here.
    expect(html).toContain("Only me");
    expect(html).toContain("Everyone");
    // No directory: not the assigned names, not the picker, not the picker's options.
    expect(html).not.toContain("Colleague");
    expect(html).not.toContain("colleague@example.com");
    expect(html).not.toContain("Stranger");
    expect(html).not.toContain("<select");
  });

  it("shows the assignment list to an admin on a restricted agent", () => {
    const html = render(true, "restricted");
    expect(html).toContain("Colleague");
    expect(html).toContain("Stranger");
    expect(html).toContain("<select");
  });

  it("drops the assignment list when the agent is open to everyone", () => {
    const html = render(true, "system");
    expect(html).toContain("This agent is open to everyone");
    expect(html).not.toContain("Colleague");
    expect(html).not.toContain("<select");
  });
});
