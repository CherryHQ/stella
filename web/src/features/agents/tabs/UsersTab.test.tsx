import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import type { User } from "@/lib/types";
import type { AgentsPageState } from "../agent-detail-state";
import { UsersTab } from "./UsersTab";

vi.hoisted(() => {
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: { getItem: () => "en", setItem: () => undefined },
  });
});

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key, locale: "en", setLocale: vi.fn() }),
}));

const assignedUsers = [{ id: "u2", email: "colleague@example.com", name: "Colleague" }] as User[];
const availableUsers = [{ id: "u3", email: "stranger@example.com", name: "Stranger" }] as User[];

function render(isAdmin: boolean, scope: string) {
  const state = {
    isAdmin,
    assignedUsers,
    addUserId: "",
    form: { scope },
  } as unknown as AgentsPageState;
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
    expect(html).toContain("agents.users.scopeRestricted");
    expect(html).toContain("agents.users.scopeSystem");
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
    expect(html).toContain("agents.users.systemHint");
    expect(html).not.toContain("Colleague");
    expect(html).not.toContain("<select");
  });
});
