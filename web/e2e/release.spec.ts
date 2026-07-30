import { Buffer } from "node:buffer";
import { writeFile } from "node:fs/promises";

import {
  expect,
  test as base,
  type APIResponse,
  type Locator,
  type Page,
  type Response,
} from "@playwright/test";

const env = {
  adminEmail: requiredEnv("STELLA_E2E_ADMIN_EMAIL"),
  adminPassword: requiredEnv("STELLA_E2E_ADMIN_PASSWORD"),
  userEmail: requiredEnv("STELLA_E2E_USER_EMAIL"),
  userPassword: requiredEnv("STELLA_E2E_USER_PASSWORD"),
  userID: requiredEnv("STELLA_E2E_USER_ID"),
  signupEmail: requiredEnv("STELLA_E2E_SIGNUP_EMAIL"),
  signupPassword: requiredEnv("STELLA_E2E_SIGNUP_PASSWORD"),
  fakeProviderURL: requiredEnv("STELLA_E2E_FAKE_PROVIDER_URL"),
  fixturePrefix: requiredEnv("STELLA_E2E_FIXTURE_PREFIX"),
  secretProbe: requiredEnv("STELLA_E2E_SECRET_PROBE"),
};

// A tiny deterministic archive with one root SKILL.md. Keeping it inline makes
// the Browser runner independent of network registries and untracked fixtures.
const RELEASE_SKILL_ZIP = Buffer.from(
  "UEsDBBQAAAAIALie/Vyp/Y+vbQAAAJYAAAAIAAAAU0tJTEwubWRcjTEOg0AMBHu/wlJq8wDKKA0tecFxcbHKYSLbiO+joEuTdjWzIyJkZdWRXZuWUFl8O0Jd4o3W6KVRHZ/EZiM/NNVXGCJRuYM/kS9hIBEhuvHc13unntcdTYZEaf81hkX6Xr+dGOgEAAD//wMAUEsBAhQAFAAAAAgAuJ79XKn9j69tAAAAlgAAAAgAAAAAAAAAAAAAAAAAAAAAAFNLSUxMLm1kUEsFBgAAAAABAAEANgAAAJMAAAAAAA==",
  "base64",
);

type DiagnosticFixtures = {
  diagnostics: void;
};

const test = base.extend<DiagnosticFixtures>({
  diagnostics: [
    async ({ page }, use, testInfo) => {
      const consoleEntries: Array<{ type: string; text: string }> = [];
      const networkEntries: Array<{
        method: string;
        url: string;
        status: number;
        resource_type: string;
      }> = [];

      page.on("console", (message) => {
        consoleEntries.push({
          type: message.type(),
          text: redactEphemeralValues(message.text()),
        });
      });
      page.on("pageerror", (error) => {
        consoleEntries.push({
          type: "pageerror",
          text: redactEphemeralValues(error.message),
        });
      });
      page.on("response", (response) => {
        const url = new URL(response.url());
        networkEntries.push({
          method: response.request().method(),
          url: `${url.origin}${url.pathname}`,
          status: response.status(),
          resource_type: response.request().resourceType(),
        });
      });

      try {
        await use();
      } finally {
        // Keep diagnostics useful without retaining request bodies, headers,
        // query strings, or unbounded browser chatter. finally guarantees the
        // attachments survive assertion and whole-test timeouts.
        const consolePath = testInfo.outputPath("browser-console.json");
        const networkPath = testInfo.outputPath("network-summary.json");
        await writeFile(consolePath, `${JSON.stringify(consoleEntries.slice(-2_000), null, 2)}\n`);
        await writeFile(networkPath, `${JSON.stringify(networkEntries.slice(-5_000), null, 2)}\n`);
        await testInfo.attach("browser-console.json", {
          path: consolePath,
          contentType: "application/json",
        });
        await testInfo.attach("network-summary.json", {
          path: networkPath,
          contentType: "application/json",
        });
      }
    },
    { auto: true },
  ],
});

test("[C02-S02] authenticate through the browser", async ({ page }) => {
  await page.goto("/signup");
  await page.getByLabel("Name").fill("Release Browser User");
  await page.getByLabel("Email").fill(env.signupEmail);
  await page.getByLabel("Password", { exact: true }).fill("short");
  await page.getByLabel("Confirm Password").fill("short");
  await page.getByRole("button", { name: "Sign up", exact: true }).click();
  await expect(page.getByText("Password must be at least 8 characters long")).toBeVisible();

  await page.getByLabel("Password", { exact: true }).fill(env.signupPassword);
  await page.getByLabel("Confirm Password").fill(`${env.signupPassword}-mismatch`);
  await page.getByRole("button", { name: "Sign up", exact: true }).click();
  await expect(page.getByText("Passwords do not match")).toBeVisible();

  await page.getByLabel("Confirm Password").fill(env.signupPassword);
  const registration = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/auth/local/register") && response.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Sign up", exact: true }).click();
  expect((await registration).status()).toBe(200);
  await expect(page).not.toHaveURL(/\/signup(?:[/?#]|$)/);

  await logout(page);
  const failedLogin = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/auth/local/login") && response.request().method() === "POST",
  );
  await fillLogin(page, env.signupEmail, `${env.signupPassword}-wrong`);
  expect((await failedLogin).status()).toBe(401);
  await expect(page.getByText(/invalid email or password/i)).toBeVisible();

  await fillLogin(page, env.signupEmail, env.signupPassword);
  await expect(page).not.toHaveURL(/\/login(?:[/?#]|$)/);
  await logout(page);
  await expect(page).toHaveURL(/\/login(?:[/?#]|$)/);
});

test("[C03-S02] administer a user while privileged controls fail closed", async ({ page }) => {
  await login(page, env.adminEmail, env.adminPassword);
  await page.goto("/settings/users");
  await page.getByText(env.userEmail, { exact: true }).click();

  const rolePromoted = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname.endsWith("/role") && response.request().method() === "PATCH",
  );
  await page.getByRole("button", { name: "Promote to admin", exact: true }).click();
  expect((await rolePromoted).ok()).toBeTruthy();
  await expect(page.getByRole("button", { name: "Demote to user", exact: true })).toBeVisible();

  const roleDemoted = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname.endsWith("/role") && response.request().method() === "PATCH",
  );
  await page.getByRole("button", { name: "Demote to user", exact: true }).click();
  expect((await roleDemoted).ok()).toBeTruthy();
  await expect(page.getByRole("button", { name: "Promote to admin", exact: true })).toBeVisible();

  const deactivated = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname.endsWith("/active") &&
      response.request().method() === "PATCH",
  );
  await page.getByRole("button", { name: "Deactivate", exact: true }).click();
  expect((await deactivated).ok()).toBeTruthy();
  await expect(page.getByRole("button", { name: "Activate", exact: true })).toBeVisible();

  const activated = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname.endsWith("/active") &&
      response.request().method() === "PATCH",
  );
  await page.getByRole("button", { name: "Activate", exact: true }).click();
  expect((await activated).ok()).toBeTruthy();
  await expect(page.getByRole("button", { name: "Deactivate", exact: true })).toBeVisible();

  const userSheet = page.locator('[data-slot="sheet-popup"]');
  await userSheet.getByRole("button", { name: "Close", exact: true }).click();
  await expect(userSheet).toBeHidden();
  await logout(page);
  await login(page, env.userEmail, env.userPassword);
  const forbiddenList = await page.request.get("/api/users");
  expect(forbiddenList.status()).toBe(403);
  await page.goto("/settings/users");
  await expect(page.getByText(env.adminEmail, { exact: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Promote to admin", exact: true })).toHaveCount(0);
});

test("[C05-S02] manage an agent and its user permissions", async ({ page }) => {
  await login(page, env.adminEmail, env.adminPassword);
  await page.goto("/settings/agents");
  await page.getByRole("button", { name: "New agent", exact: true }).click();
  // The template card renders as a button, but Base UI does not currently
  // expose its nested copy as the button's accessible name.
  await page.getByText("Blank", { exact: true }).click();

  const agentName = `${env.fixturePrefix}-agent-ui`;
  await page.getByPlaceholder("My Agent").fill(agentName);
  await selectNearLabel(page, "Scope").selectOption("restricted");
  const createdResponse = page.waitForResponse(
    (response) => response.url().endsWith("/api/agents") && response.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Create", exact: true }).click();
  const created = await jsonFrom<{ id: string }>(await createdResponse);
  expect(created.id).toBeTruthy();
  await expect(page.getByText("Saved", { exact: true })).toBeVisible();

  const editedName = `${agentName}-edited`;
  await page.getByPlaceholder("My Agent").fill(editedName);
  const updatedResponse = page.waitForResponse(
    (response) =>
      response.url().endsWith(`/api/agents/${created.id}`) &&
      response.request().method() === "PATCH",
  );
  await page.getByRole("button", { name: "Update", exact: true }).click();
  expect((await updatedResponse).ok()).toBeTruthy();
  await expect(page.getByText("Saved", { exact: true })).toBeVisible();

  await page.getByRole("tab", { name: "Users", exact: true }).click();
  const userSelect = page.locator("select").filter({
    has: page.locator(`option[value="${env.userID}"]`),
  });
  await userSelect.selectOption(env.userID);
  const assignedResponse = page.waitForResponse(
    (response) =>
      response.url().endsWith(`/api/agents/${created.id}/users`) &&
      response.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Add", exact: true }).click();
  expect((await assignedResponse).ok()).toBeTruthy();
  await expect(page.getByText("User assigned", { exact: true })).toBeVisible();
  await expect(page.getByText(env.userEmail, { exact: true })).toBeVisible();

  await logout(page);
  await login(page, env.userEmail, env.userPassword);
  await page.goto(`/settings/agents/${created.id}/config`);
  await expect(page.getByText(editedName, { exact: false })).toBeVisible();
  await expect(page.getByRole("button", { name: "Update", exact: true })).toHaveCount(0);
  await expect(page.getByRole("tab", { name: "Users", exact: true })).toHaveCount(0);
});

test("[C06-S02] configure a provider and mask its secret", async ({ page }) => {
  await login(page, env.adminEmail, env.adminPassword);
  const providerID = `${env.fixturePrefix}-provider-ui`;
  const apiKey = `${env.fixturePrefix}-ephemeral-api-key`;

  await page.goto("/settings/providers/new");
  await selectNearLabel(page, "Type").selectOption("anthropic");
  await page.getByPlaceholder("e.g. openrouter").fill(providerID);
  await inputNearLabel(page, "Display name").fill("Release Browser Provider");
  await page.getByRole("button", { name: "Add provider", exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`/settings/providers/${providerID}$`));

  const apiKeyInput = inputNearLabel(page, "API Key");
  const baseURLInput = inputNearLabel(page, "Base URL");
  await apiKeyInput.fill(apiKey);
  await baseURLInput.fill("http://127.0.0.1:1");
  const failedFetch = page.waitForResponse(isProviderModelFetch(providerID));
  await page.getByRole("button", { name: "Fetch models", exact: true }).click();
  expect((await failedFetch).ok()).toBeFalsy();

  await baseURLInput.fill(env.fakeProviderURL);
  const successfulFetch = page.waitForResponse(isProviderModelFetch(providerID));
  await page.getByRole("button", { name: "Fetch models", exact: true }).click();
  expect((await successfulFetch).ok()).toBeTruthy();
  await expect(page.getByText("claude-release-browser", { exact: true })).toBeVisible();

  const enabledSwitch = page
    .getByText("Enabled", { exact: true })
    .first()
    .locator("..")
    .getByRole("switch");
  if ((await enabledSwitch.getAttribute("aria-checked")) !== "true") {
    await enabledSwitch.click();
  }
  const savedResponse = page.waitForResponse(
    (response) =>
      response.url().endsWith(`/api/providers/${providerID}`) &&
      response.request().method() === "PATCH",
  );
  await page.getByRole("button", { name: "Save", exact: true }).click();
  expect((await savedResponse).ok()).toBeTruthy();

  await page.reload();
  await expect(inputNearLabel(page, "API Key")).toHaveAttribute("type", "password");
  await expect(inputNearLabel(page, "API Key")).toHaveValue(apiKey);
  await expect(page.locator("body")).not.toContainText(apiKey);

  await page.getByRole("button", { name: "Delete", exact: true }).click();
  const deleteDialog = page.getByRole("dialog");
  await expect(deleteDialog.getByText("Delete provider?", { exact: true })).toBeVisible();
  const deletedResponse = page.waitForResponse(
    (response) =>
      response.url().endsWith(`/api/providers/${providerID}`) &&
      response.request().method() === "DELETE",
  );
  await deleteDialog.getByRole("button", { name: "Delete", exact: true }).click();
  expect((await deletedResponse).status()).toBe(204);
});

test("[C07-S03] stream and restore a chat session", async ({ page }) => {
  await login(page, env.adminEmail, env.adminPassword);
  const { agentID } = await createModelFixture(page, "chat");

  await page.goto(`/agents/${agentID}`);
  await expect(page).toHaveURL(new RegExp(`/agents/${agentID}/sessions/[^/]+$`));
  const originalURL = page.url();
  const sessionCreated = page.waitForResponse(
    (response) =>
      response.url().endsWith(`/api/agents/${agentID}/sessions`) &&
      response.request().method() === "POST",
  );
  await page.getByTitle("New thread").click();
  expect((await sessionCreated).status()).toBe(201);
  await expect(page).not.toHaveURL(originalURL);

  const firstChunk = "release browser ";
  const fullReply = `${firstChunk}reply`;
  const userMessage = `${env.fixturePrefix} streaming message`;
  const gate = await page.request.post(`${env.fakeProviderURL}/control/gate`);
  expect(gate.ok()).toBeTruthy();

  await page.getByPlaceholder("Message…").fill(userMessage);
  await page.getByTitle("Send message").click();
  await expect(page.getByText(firstChunk, { exact: false })).toBeVisible();
  await expect(page.getByText(fullReply, { exact: false })).toHaveCount(0);

  const release = await page.request.post(`${env.fakeProviderURL}/control/release`);
  expect(release.ok()).toBeTruthy();
  await expect(page.getByText(fullReply, { exact: false })).toBeVisible();

  await page.reload();
  await expect(page.getByText(userMessage, { exact: false })).toBeVisible();
  await expect(page.getByText(fullReply, { exact: false })).toBeVisible();

  // A provider failure must be visible in the transcript, and the next turn
  // must clear that run-level error instead of leaving the chat stuck.
  const failNext = await page.request.post(`${env.fakeProviderURL}/control/error`);
  expect(failNext.ok()).toBeTruthy();
  await page.getByPlaceholder("Message…").fill(`${env.fixturePrefix} failing message`);
  await page.getByTitle("Send message").click();
  await expect(page.getByText("Response failed", { exact: true })).toBeVisible();

  await page.getByPlaceholder("Message…").fill(`${env.fixturePrefix} recovery message`);
  await page.getByTitle("Send message").click();
  await expect(page.getByText("Response failed", { exact: true })).toHaveCount(0);
  await expect(page.getByText(fullReply, { exact: false }).last()).toBeVisible();
});

test("[C10-S03] create and intervene in a goal through the browser", async ({ page }) => {
  await login(page, env.adminEmail, env.adminPassword);
  const agent = await requestJSON<{ id: string }>(page, "POST", "/api/agents", {
    name: `${env.fixturePrefix}-goal-ui-agent`,
    enabled: true,
  });
  const title = `${env.fixturePrefix} browser goal`;
  const intent = "Verify the draft, cancellation, archive, and restore lifecycle.";

  await page.goto(`/agents/${agent.id}/goals/new`);
  await page.getByPlaceholder("What should the agent deliver?").fill(title);
  await page.getByPlaceholder("What does acceptance look like?").fill(intent);
  const createdResponse = page.waitForResponse(
    (response) => response.url().endsWith("/api/goals") && response.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Create", exact: true }).click();
  const goal = await jsonFrom<{ id: string }>(await createdResponse);
  await expect(page).toHaveURL(new RegExp(`/agents/${agent.id}/goals/${goal.id}$`));
  await expect(
    page.getByRole("heading", {
      name: new RegExp(`^${escapeRegExp(title)}\\s+Draft$`),
    }),
  ).toBeVisible();

  const cancelled = page.waitForResponse(
    (response) =>
      response.url().endsWith(`/api/goals/${goal.id}/cancel`) &&
      response.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  expect((await cancelled).ok()).toBeTruthy();
  await expect(
    page.getByRole("heading", {
      name: new RegExp(`^${escapeRegExp(title)}\\s+Cancelled$`),
    }),
  ).toBeVisible();

  const archived = page.waitForResponse(
    (response) =>
      response.url().endsWith(`/api/goals/${goal.id}`) && response.request().method() === "DELETE",
  );
  await page.getByRole("button", { name: "Archive", exact: true }).click();
  expect((await archived).status()).toBe(204);
  await expect(page.getByRole("button", { name: "Restore", exact: true })).toBeVisible();

  const restored = page.waitForResponse(
    (response) =>
      response.url().endsWith(`/api/goals/${goal.id}/unarchive`) &&
      response.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Restore", exact: true }).click();
  expect((await restored).ok()).toBeTruthy();
  await expect(page.getByRole("button", { name: "Archive", exact: true })).toBeVisible();
});

test("[C11-S02] save and run a workflow through the browser", async ({ page }) => {
  await login(page, env.adminEmail, env.adminPassword);
  const { agentID } = await createModelFixture(page, "workflow");
  const goal = await requestJSON<{ id: string }>(page, "POST", "/api/goals", {
    title: `${env.fixturePrefix} workflow source`,
    intent: "Produce a frozen one-child workflow plan.",
    agent_id: agentID,
    kind: "composite",
    review_policy: "none",
  });
  await expect
    .poll(
      async () => {
        const current = await requestJSON<{
          lifecycle: string;
          done_reason?: string;
          acceptance_state?: string;
        }>(page, "GET", `/api/goals/${goal.id}`);
        return `${current.lifecycle}/${current.done_reason}/${current.acceptance_state}`;
      },
      { timeout: 120_000 },
    )
    .toBe("done/accepted/passed");

  await page.goto(`/agents/${agentID}/goals/${goal.id}`);
  await page.getByRole("button", { name: "Save as workflow", exact: true }).click();
  const saveDialog = page.getByRole("dialog");
  const workflowName = `${env.fixturePrefix}-workflow`;
  await saveDialog.locator("input").first().fill(workflowName);
  const savedResponse = page.waitForResponse(
    (response) =>
      response.url().endsWith(`/api/goals/${goal.id}/save-as-workflow`) &&
      response.request().method() === "POST",
  );
  await saveDialog.getByRole("button", { name: "Save", exact: true }).click();
  const workflow = await jsonFrom<{ id: string }>(await savedResponse);
  const workflowPath = `/agents/${agentID}/workflows/${workflow.id}`;
  await expect(page).toHaveURL(new RegExp(`${workflowPath}$`));
  await expect(page.getByText(workflowName, { exact: true }).first()).toBeVisible();
  await expect(page.getByText("Release browser child", { exact: true })).toBeVisible();

  await page.getByRole("button", { name: "Run", exact: true }).click();
  const runDialog = page.getByRole("dialog");
  const runResponse = page.waitForResponse(
    (response) =>
      response.url().endsWith(`/api/workflows/${workflow.id}/instantiate`) &&
      response.request().method() === "POST",
  );
  await runDialog.getByRole("button", { name: "Run", exact: true }).click();
  expect((await runResponse).status()).toBe(201);
  await expect(page).toHaveURL(new RegExp(`/agents/${agentID}/goals/[^/]+$`));

  await page.goto(workflowPath);
  await expect(page.getByText("Workflow runs", { exact: true })).toBeVisible();
  await expect(page.getByText(/run #1/i)).toBeVisible();
});

test("[C12-S03] manage and trigger a schedule through the browser", async ({ page }) => {
  await login(page, env.adminEmail, env.adminPassword);
  const { agentID } = await createModelFixture(page, "schedule");
  const scheduleName = `${env.fixturePrefix} daily schedule`;

  await page.goto(`/agents/${agentID}/goals/new`);
  await page.getByRole("button", { name: "New schedule", exact: true }).click();
  await page.getByPlaceholder("Short label for this schedule").fill(scheduleName);
  await page.getByPlaceholder("What should the agent do?").fill("Return the release marker.");
  const createdResponse = page.waitForResponse(
    (response) =>
      response.url().endsWith(`/api/agents/${agentID}/scheduler/jobs`) &&
      response.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Create", exact: true }).click();
  const schedule = await jsonFrom<{ id: string }>(await createdResponse);
  await expect(page).toHaveURL(new RegExp(`/agents/${agentID}/goals/schedules/${schedule.id}$`));
  await expect(
    page.getByRole("heading", {
      name: new RegExp(`^${escapeRegExp(scheduleName)}\\s+Enabled$`),
    }),
  ).toBeVisible();

  const triggered = page.waitForResponse(
    (response) =>
      response.url().endsWith(`/api/agents/${agentID}/scheduler/jobs/${schedule.id}/run`) &&
      response.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Run Now", exact: true }).click();
  expect((await triggered).status()).toBe(202);
  await expect(page.getByText("Run triggered", { exact: true })).toBeVisible();
  await expect
    .poll(
      async () => {
        const list = await requestJSON<{ runs: Array<{ status: string }> }>(
          page,
          "GET",
          `/api/agents/${agentID}/scheduler/jobs/${schedule.id}/runs`,
        );
        return list.runs[0]?.status;
      },
      { timeout: 60_000 },
    )
    .toBe("success");
  await page.reload();
  await expect(page.getByText("success", { exact: true }).filter({ visible: true })).toBeVisible();

  const paused = page.waitForResponse(
    (response) =>
      response.url().endsWith(`/api/agents/${agentID}/scheduler/jobs/${schedule.id}`) &&
      response.request().method() === "PATCH",
  );
  await page.getByRole("button", { name: "Pause", exact: true }).click();
  expect((await paused).ok()).toBeTruthy();
  await expect(page.getByText("Disabled", { exact: true })).toBeVisible();

  const resumed = page.waitForResponse(
    (response) =>
      response.url().endsWith(`/api/agents/${agentID}/scheduler/jobs/${schedule.id}`) &&
      response.request().method() === "PATCH",
  );
  await page.getByRole("button", { name: "Resume", exact: true }).click();
  expect((await resumed).ok()).toBeTruthy();
  await expect(page.getByText("Enabled", { exact: true })).toBeVisible();

  await page.getByRole("button", { name: "Edit", exact: true }).click();
  const editSheet = page.locator('[data-slot="sheet-popup"]');
  const editedName = `${scheduleName} edited`;
  await editSheet.locator("input").first().fill(editedName);
  const edited = page.waitForResponse(
    (response) =>
      response.url().endsWith(`/api/agents/${agentID}/scheduler/jobs/${schedule.id}`) &&
      response.request().method() === "PATCH",
  );
  await editSheet.getByRole("button", { name: "Save", exact: true }).click();
  expect((await edited).ok()).toBeTruthy();
  await expect(
    page.getByRole("heading", {
      name: new RegExp(`^${escapeRegExp(editedName)}\\s+Enabled$`),
    }),
  ).toBeVisible();

  await page.getByRole("button", { name: "Edit", exact: true }).click();
  await editSheet.getByRole("button", { name: "Delete this schedule…", exact: true }).click();
  const deleteDialog = page.getByRole("dialog");
  await expect(deleteDialog.getByText("Delete this schedule?", { exact: true })).toBeVisible();
  const deleted = page.waitForResponse(
    (response) =>
      response.url().endsWith(`/api/agents/${agentID}/scheduler/jobs/${schedule.id}`) &&
      response.request().method() === "DELETE",
  );
  await deleteDialog.getByRole("button", { name: "Delete", exact: true }).click();
  expect((await deleted).status()).toBe(204);
  await expect(page).toHaveURL(new RegExp(`/agents/${agentID}/goals$`));
});

test("[C13-S03] edit memory surfaces and inspect their history", async ({ page }) => {
  await login(page, env.adminEmail, env.adminPassword);
  const agent = await requestJSON<{ id: string }>(page, "POST", "/api/agents", {
    name: `${env.fixturePrefix}-memory-agent`,
    enabled: true,
  });
  await page.goto(`/agents/${agent.id}/memories`);

  const soul = `${env.fixturePrefix} agent soul`;
  const soulSection = memorySection(page, "Agent Soul");
  await soulSection.getByRole("button", { name: "Edit", exact: true }).click();
  await soulSection.locator("textarea").fill(soul);
  await soulSection.getByRole("button", { name: "Save", exact: true }).click();
  await expect(soulSection.getByText(soul, { exact: true })).toBeVisible();

  const profile = `${env.fixturePrefix} user profile`;
  const profileSection = memorySection(page, "User Profile");
  await profileSection.getByRole("button", { name: "Edit", exact: true }).click();
  await profileSection.locator("textarea").fill(profile);
  await profileSection.getByRole("button", { name: "Save", exact: true }).click();
  await expect(profileSection.getByText(profile, { exact: true })).toBeVisible();

  const knowledge = `${env.fixturePrefix} durable knowledge`;
  const knowledgeSection = memorySection(page, "Knowledge");
  await knowledgeSection.getByRole("button", { name: "Add", exact: true }).click();
  const knowledgeDialog = page.getByRole("dialog");
  await expect(knowledgeDialog.getByText("Add Knowledge", { exact: true })).toBeVisible();
  await knowledgeDialog.locator("textarea").fill(knowledge);
  await knowledgeDialog.getByRole("button", { name: "Add", exact: true }).click();
  await expect(knowledgeSection.getByText(knowledge, { exact: true })).toBeVisible();

  const constraint = `${env.fixturePrefix} hard constraint`;
  const constraintsSection = memorySection(page, "Constraints");
  await constraintsSection.getByText("Constraints", { exact: true }).click();
  await constraintsSection.getByPlaceholder("Add a hard rule…").fill(constraint);
  await constraintsSection.getByRole("button", { name: "Add", exact: true }).click();
  await expect(constraintsSection.getByText(constraint, { exact: true })).toBeVisible();

  const changelogSection = memorySection(page, "Recent Changes");
  await changelogSection.getByText("Recent Changes", { exact: true }).click();
  await expect(changelogSection.getByText("Soul", { exact: true }).first()).toBeVisible();
  await expect(changelogSection.getByText("Profile", { exact: true }).first()).toBeVisible();
  await expect(changelogSection.getByText("Knowledge", { exact: true }).first()).toBeVisible();
  await expect(changelogSection.getByText("Constraint", { exact: true }).first()).toBeVisible();
});

test("[C14-S02] upload edit and remove a skill through the browser", async ({ page }) => {
  await login(page, env.adminEmail, env.adminPassword);
  const agent = await requestJSON<{ id: string }>(page, "POST", "/api/agents", {
    name: `${env.fixturePrefix}-skill-agent`,
    enabled: true,
  });
  await page.goto(`/agents/${agent.id}/skills?source=manual`);
  await page.locator('input[type="file"]').setInputFiles({
    name: "release-browser-skill.zip",
    mimeType: "application/zip",
    buffer: RELEASE_SKILL_ZIP,
  });
  const uploaded = page.waitForResponse(
    (response) =>
      response.url().includes(`/api/agents/${agent.id}/skills`) &&
      response.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Upload ZIP", exact: true }).click();
  expect((await uploaded).status()).toBe(201);
  await expect(page.getByText("release-browser-skill", { exact: true }).first()).toBeVisible();

  const drawer = page.locator('[data-slot="sheet-popup"]');
  await drawer.getByRole("button", { name: "SKILL.md", exact: true }).click();
  await drawer.getByRole("button", { name: "Edit", exact: true }).click();
  const updatedContent = [
    "---",
    "name: release-browser-skill",
    "description: Deterministic browser release skill.",
    "---",
    "",
    "# Release Browser Skill",
    "",
    `${env.fixturePrefix} edited instructions.`,
  ].join("\n");
  await drawer.locator("textarea").fill(updatedContent);
  const updated = page.waitForResponse(
    (response) =>
      response.url().includes(`/api/agents/${agent.id}/skills/`) &&
      response.request().method() === "PATCH",
  );
  await drawer.getByRole("button", { name: "Save", exact: true }).click();
  expect((await updated).ok()).toBeTruthy();
  await expect(page.getByText("Settings saved", { exact: true })).toBeVisible();

  // Saving a file returns the drawer to the skill detail view.
  await drawer.getByRole("button", { name: "Delete skill", exact: true }).click();
  const deleteDialog = page.getByRole("alertdialog");
  const deleted = page.waitForResponse(
    (response) =>
      response.url().includes(`/api/agents/${agent.id}/skills/`) &&
      response.request().method() === "DELETE",
  );
  await deleteDialog.getByRole("button", { name: "Delete skill", exact: true }).click();
  expect((await deleted).status()).toBe(204);
  await expect(page.getByText("Skill deleted", { exact: true })).toBeVisible();
});

test("[C16-S02] open an actionable inbox item through the browser", async ({ page }) => {
  await login(page, env.adminEmail, env.adminPassword);
  const { agentID } = await createModelFixture(page, "inbox");
  const title = `${env.fixturePrefix} review inbox goal`;
  const goal = await requestJSON<{ id: string }>(page, "POST", "/api/goals", {
    title,
    intent: "Produce a deterministic plan that requires human approval.",
    agent_id: agentID,
    kind: "composite",
    review_policy: "human",
  });
  await expect
    .poll(
      async () => {
        const current = await requestJSON<{ lifecycle: string; block_reason?: string }>(
          page,
          "GET",
          `/api/goals/${goal.id}`,
        );
        return `${current.lifecycle}/${current.block_reason}`;
      },
      { timeout: 90_000 },
    )
    .toBe("blocked/needs_plan_approval");

  await page.goto("/inbox");
  await expect(page.getByText(title, { exact: true })).toBeVisible();
  await expect(page.getByText("Review", { exact: true }).first()).toBeVisible();
  await page.getByRole("link", { name: "Open", exact: true }).first().click();
  await expect(page).toHaveURL(new RegExp(`/agents/${agentID}/goals/${goal.id}$`));
  await expect(
    page.getByRole("heading", {
      name: new RegExp(`^${escapeRegExp(title)}\\s+Blocked$`),
    }),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "Approve", exact: true })).toBeVisible();
});

test("[C17-S02] share and revoke an artifact", async ({ browser, page }) => {
  await login(page, env.adminEmail, env.adminPassword);
  const agent = await requestJSON<{ id: string }>(page, "POST", "/api/agents", {
    name: `${env.fixturePrefix}-share-agent`,
    enabled: true,
  });
  const session = await requestJSON<{ id: string }>(
    page,
    "POST",
    `/api/agents/${agent.id}/sessions`,
    { kind: "chat" },
  );
  const fileName = `${env.fixturePrefix}-artifact.md`;
  const heading = "Release Browser Artifact";
  await requestJSON(
    page,
    "POST",
    `/api/agents/${agent.id}/sessions/${session.id}/workspace/files?scope=agent`,
    {
      path: fileName,
      content: `# ${heading}\n\nShared by the release suite.\n`,
    },
  );

  await page.goto(`/agents/${agent.id}/sessions/${session.id}`);
  await page.getByRole("button", { name: "Show workspace", exact: true }).click();
  // The tree visually splits long filenames across spans, while its
  // accessible treeitem name remains the complete path.
  const fileEntry = page.getByRole("treeitem", {
    name: fileName,
    exact: true,
  });
  await expect(fileEntry).toBeVisible();
  await fileEntry.click({ button: "right" });
  const contextMenu = page.locator('[data-file-tree-context-menu-root="true"]');
  await contextMenu.getByRole("button", { name: "Share", exact: true }).click();

  const shareDialog = page.getByRole("dialog");
  await expect(shareDialog.getByText("Share artifact", { exact: true })).toBeVisible();
  await shareDialog.getByRole("button", { name: "Create link", exact: true }).click();
  const shareURL = await shareDialog.locator("input[readonly]").inputValue();
  expect(new URL(shareURL).pathname).toMatch(/^\/s\/[^/]+$/);

  const anonymousContext = await browser.newContext({ locale: "en-US" });
  const anonymousPage = await anonymousContext.newPage();
  try {
    await anonymousPage.goto(shareURL);
    await expect(anonymousPage.locator("iframe")).toBeVisible();
    await expect(anonymousPage.frameLocator("iframe").getByText(heading)).toBeVisible();

    await shareDialog.getByRole("button", { name: "Revoke link", exact: true }).click();
    await expect(
      shareDialog.getByRole("button", { name: "Create link", exact: true }),
    ).toBeVisible();
    await anonymousPage.reload();
    await expect(anonymousPage.getByText("Share unavailable", { exact: true })).toBeVisible();
    await expect(
      anonymousPage.getByText("Share not found or expired", { exact: true }),
    ).toBeVisible();
  } finally {
    await anonymousContext.close();
  }
});

test("[C18-S02] manage a secret without retaining its value in diagnostics", async ({ page }) => {
  await login(page, env.adminEmail, env.adminPassword);
  const secretName = `${env.fixturePrefix.replaceAll("-", "_").toUpperCase()}_SECRET`;
  await page.goto("/settings/credentials");
  await page.getByRole("button", { name: "Add Secret", exact: true }).click();
  const secretSheet = page.locator('[data-slot="sheet-popup"]');
  await secretSheet.getByPlaceholder("e.g. MY_API_KEY").fill(secretName);
  const valueInput = secretSheet.getByPlaceholder("secret value");
  await expect(valueInput).toHaveAttribute("type", "password");
  await valueInput.fill(env.secretProbe);
  const created = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname.includes(`/api/vault/${secretName}`) &&
      ["PUT", "POST"].includes(response.request().method()),
  );
  await secretSheet.getByRole("button", { name: "Add Secret", exact: true }).click();
  expect((await created).ok()).toBeTruthy();
  await expect(page.getByText("Secret saved", { exact: true })).toBeVisible();
  await expect(page.locator("body")).not.toContainText(env.secretProbe);

  await page.getByText(secretName, { exact: true }).click();
  const editValue = secretSheet.getByPlaceholder("leave blank to keep existing value");
  await expect(editValue).toHaveAttribute("type", "password");
  await expect(editValue).toHaveValue("");
  await editValue.fill(`${env.secretProbe}-rotated`);
  const rotated = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname.includes(`/api/vault/${secretName}`) &&
      ["PUT", "POST"].includes(response.request().method()),
  );
  await secretSheet.getByRole("button", { name: "Save", exact: true }).click();
  expect((await rotated).ok()).toBeTruthy();
  await expect(page.locator("body")).not.toContainText(env.secretProbe);

  const secretRow = page
    .getByText(secretName, { exact: true })
    .locator("xpath=ancestor::div[.//button[@aria-label='More actions']][1]");
  await secretRow.getByLabel("More actions").click();
  await page.getByRole("menuitem", { name: "Delete", exact: true }).click();
  const deleteDialog = page.getByRole("dialog");
  await expect(deleteDialog.getByText("Delete secret", { exact: true })).toBeVisible();
  const deleted = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname.includes(`/api/vault/${secretName}`) &&
      response.request().method() === "DELETE",
  );
  await deleteDialog.getByRole("button", { name: "Delete", exact: true }).click();
  expect((await deleted).status()).toBe(204);
  await expect(page.getByText(secretName, { exact: true })).toHaveCount(0);
});

test("[X10-S02] inspect and toggle a built-in plugin through the browser", async ({ page }) => {
  await login(page, env.adminEmail, env.adminPassword);
  await page.goto("/settings/plugins/webfetch");
  const pluginSheet = page.locator('[data-slot="sheet-popup"]');
  await expect(pluginSheet.getByText("tool/webfetch", { exact: true })).toBeVisible();
  const enabledSwitch = pluginSheet.getByRole("switch");
  const initiallyEnabled = (await enabledSwitch.getAttribute("aria-checked")) === "true";

  const toggled = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/plugins/tool/webfetch") &&
      response.request().method() === "PATCH",
  );
  await enabledSwitch.click();
  expect((await toggled).ok()).toBeTruthy();
  await expect(
    page.getByText(`tool/webfetch ${initiallyEnabled ? "disabled" : "enabled"}`, { exact: true }),
  ).toBeVisible();

  await page.reload();
  await expect(pluginSheet.getByText("tool/webfetch", { exact: true })).toBeVisible();
  await expect(enabledSwitch).toHaveAttribute("aria-checked", initiallyEnabled ? "false" : "true");

  const restored = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/plugins/tool/webfetch") &&
      response.request().method() === "PATCH",
  );
  await enabledSwitch.click();
  expect((await restored).ok()).toBeTruthy();
  await expect(enabledSwitch).toHaveAttribute("aria-checked", initiallyEnabled ? "true" : "false");
});

test("[X11-S02] poll a fixture feed and use the Recally browser surfaces", async ({ page }) => {
  await login(page, env.adminEmail, env.adminPassword);
  await page.goto("/recally");
  const feedURL = `${env.fakeProviderURL}/fixtures/feed.xml`;
  const feedInput = page.getByPlaceholder("Feed URL…");
  await feedInput.fill(feedURL);
  const feedCreated = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/recally/feeds") && response.request().method() === "POST",
  );
  await feedInput.locator("..").getByRole("button").click();
  const feed = await jsonFrom<{ id: string }>(await feedCreated);
  await expect(page.getByText("Feed added", { exact: true })).toBeVisible();
  const feedRow = page
    .getByText("Release Browser Feed", { exact: true })
    .locator("xpath=ancestor::div[.//button][1]");
  const feedPolled = page.waitForResponse(
    (response) =>
      response.url().endsWith(`/api/recally/feeds/${feed.id}/poll`) &&
      response.request().method() === "POST",
  );
  await feedRow.getByRole("button").click();
  expect((await feedPolled).ok()).toBeTruthy();
  await expect(page.getByText("1 new", { exact: true })).toBeVisible();

  const articleTitle = `${env.fixturePrefix} searchable Recally article`;
  const articleBody = `# ${articleTitle}\n\nDeterministic body for the release Browser journey.`;
  await requestJSON(page, "POST", "/api/recally/articles", {
    url: `${env.fakeProviderURL}/fixtures/article`,
    canonical_url: `${env.fakeProviderURL}/fixtures/article`,
    source_type: "rss",
    title: articleTitle,
    summary: "A deterministic Recally search fixture.",
    tags: ["release-browser"],
    content: articleBody,
  });
  const digestDate = new Date().toISOString().slice(0, 10);
  const digestNarrative = `${env.fixturePrefix} stored digest narrative`;
  await requestJSON(page, "POST", "/api/recally/digests", {
    narrative: digestNarrative,
    date: digestDate,
  });

  await page.reload();
  const releaseTag = page
    .getByRole("button", { name: /^release-browser\s+1$/ })
    .filter({ visible: true });
  await expect(releaseTag).toBeVisible();
  await releaseTag.click();
  await expect(page.getByText(articleTitle, { exact: true }).first()).toBeVisible();
  await page.getByPlaceholder("Search articles…").filter({ visible: true }).fill(articleTitle);
  await expect(page.getByText(articleTitle, { exact: true }).first()).toBeVisible();
  await page.getByText(articleTitle, { exact: true }).first().click();
  await expect(
    page
      .getByRole("article")
      .getByText("Deterministic body for the release Browser journey.", { exact: true }),
  ).toBeVisible();

  await page.getByRole("button", { name: /^Digest/ }).click();
  await page.getByRole("button", { name: new RegExp(digestDate) }).click();
  await expect(
    page.getByText(digestNarrative, { exact: true }).filter({ visible: true }),
  ).toBeVisible();
});

test("[X13-S03] manage OAuth provider scopes through the browser", async ({ page }) => {
  await login(page, env.adminEmail, env.adminPassword);
  await page.goto("/settings/credentials");
  const providerID = "github";
  const defaultScopeName = "workflow";

  const configLoaded = page.waitForResponse(
    (response) =>
      response.url().endsWith(`/api/admin/oauth-providers/${providerID}/config`) &&
      response.request().method() === "GET",
  );
  await page.getByText(providerID, { exact: true }).first().click();
  expect((await configLoaded).ok()).toBeTruthy();

  const clientID = `${env.fixturePrefix}-oauth-client`;
  const clientSecret = `${env.fixturePrefix}-oauth-secret`;
  await inputNearLabel(page, "Client ID").fill(clientID);
  await inputNearLabel(page, "Client Secret").fill(clientSecret);

  const scopeSearch = page.getByPlaceholder("Search scopes");
  await scopeSearch.fill(defaultScopeName);
  const defaultScope = page.getByRole("checkbox", {
    name: defaultScopeName,
    exact: true,
  });
  await expect(defaultScope).toBeChecked();
  await defaultScope.click();
  await expect(defaultScope).not.toBeChecked();

  const customScope = `${env.fixturePrefix}:custom`;
  await page.getByPlaceholder("One per line, comma, or space").fill(customScope);
  await page.getByRole("button", { name: "Add", exact: true }).click();
  await scopeSearch.fill(customScope);
  await expect(page.getByRole("checkbox", { name: customScope, exact: true })).toBeChecked();

  const saved = page.waitForResponse(
    (response) =>
      response.url().endsWith(`/api/admin/oauth-providers/${providerID}/config`) &&
      response.request().method() === "PUT",
  );
  await page.getByRole("button", { name: "Save", exact: true }).click();
  const savedResponse = await saved;
  expect(savedResponse.ok()).toBeTruthy();
  const savedRequest = savedResponse.request().postDataJSON() as {
    scopes: string[];
  };
  expect(savedRequest.scopes).toContain(customScope);
  expect(savedRequest.scopes).not.toContain(defaultScopeName);
  await expect(page.getByText(`${providerID} credentials saved`, { exact: true })).toBeVisible();

  await page.reload();
  const reloadedConfig = page.waitForResponse(
    (response) =>
      response.url().endsWith(`/api/admin/oauth-providers/${providerID}/config`) &&
      response.request().method() === "GET",
  );
  await page.getByText(providerID, { exact: true }).first().click();
  expect((await reloadedConfig).ok()).toBeTruthy();
  await page.getByPlaceholder("Search scopes").fill(customScope);
  await expect(page.getByRole("checkbox", { name: customScope, exact: true })).toBeChecked();
  await page.getByRole("button", { name: "Restore defaults", exact: true }).click();

  const defaultsSaved = page.waitForResponse(
    (response) =>
      response.url().endsWith(`/api/admin/oauth-providers/${providerID}/config`) &&
      response.request().method() === "PUT",
  );
  await page.getByRole("button", { name: "Save", exact: true }).click();
  const defaultsResponse = await defaultsSaved;
  expect(defaultsResponse.ok()).toBeTruthy();
  const defaultsRequest = defaultsResponse.request().postDataJSON() as {
    scopes: string[];
  };
  expect(defaultsRequest.scopes).toEqual([]);

  // The suite owns this temporary provider override, so remove it before the
  // later journeys reuse the same candidate database.
  await requestJSON(page, "DELETE", `/api/admin/oauth-providers/${providerID}/config`);
});

test("[X02-S02] manage and invoke a webhook channel", async ({ page }) => {
  await login(page, env.adminEmail, env.adminPassword);
  const { agentID } = await createModelFixture(page, "webhook");
  const channelID = `${env.fixturePrefix}-webhook`;

  await page.goto("/settings/channels/new");
  await selectNearLabel(page, "Platform").selectOption("webhook");
  await page.getByPlaceholder("e.g. Feishu Coder").fill("Release Browser Webhook");
  await page.getByPlaceholder("e.g. feishu-coder").fill(channelID);
  await selectNearLabel(page, "Bound agent").selectOption(agentID);
  const waitSwitch = page
    .getByText("Wait for reply by default", { exact: true })
    .locator("..")
    .getByRole("switch");
  await waitSwitch.click();
  await selectNearLabel(page, "Session mode").selectOption("persistent");

  const channelCreated = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/channels") && response.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Add Channel", exact: true }).click();
  expect((await channelCreated).status()).toBe(201);
  await expect(page).toHaveURL(new RegExp(`/settings/channels/${channelID}$`));
  await expect(page.getByText(new RegExp(`/webhooks/${channelID}$`))).toBeVisible();

  const enabledSwitch = page
    .getByText("Enabled", { exact: true })
    .locator("..")
    .getByRole("switch");
  await expect(enabledSwitch).toHaveAttribute("aria-checked", "true");
  await enabledSwitch.click();
  await saveChannel(page, channelID);

  const pat = await requestJSON<{
    token: string;
    personal_access_token: { id: string };
  }>(page, "POST", "/api/users/me/tokens", {
    name: `${env.fixturePrefix}-webhook`,
    scopes: ["agent:write"],
    never_expires: true,
  });
  const disabledIngress = await page.request.post(`/webhooks/${channelID}?wait=true`, {
    headers: { Authorization: `Bearer ${pat.token}` },
    data: `${env.fixturePrefix} disabled ingress`,
  });
  expect(disabledIngress.status()).toBe(409);

  await enabledSwitch.click();
  await saveChannel(page, channelID);
  await page.reload();
  await expect(
    page.getByText("Enabled", { exact: true }).locator("..").getByRole("switch"),
  ).toHaveAttribute("aria-checked", "true");
  await expect(
    page.getByText("Wait for reply by default", { exact: true }).locator("..").getByRole("switch"),
  ).toHaveAttribute("aria-checked", "true");
  await expect(selectNearLabel(page, "Session mode")).toHaveValue("persistent");

  const emptyIngress = await page.request.post(`/webhooks/${channelID}?wait=true`, {
    headers: { Authorization: `Bearer ${pat.token}` },
    data: "",
  });
  expect(emptyIngress.status()).toBe(400);
  const invalidWait = await page.request.post(`/webhooks/${channelID}?wait=invalid`, {
    headers: { Authorization: `Bearer ${pat.token}` },
    data: `${env.fixturePrefix} invalid wait`,
  });
  expect(invalidWait.status()).toBe(400);
  const oversizedIngress = await page.request.post(`/webhooks/${channelID}?wait=true`, {
    headers: { Authorization: `Bearer ${pat.token}` },
    data: "x".repeat(256 * 1024 + 1),
  });
  expect(oversizedIngress.status()).toBe(413);

  const ingress = await page.request.post(`/webhooks/${channelID}?wait=true`, {
    headers: { Authorization: `Bearer ${pat.token}` },
    data: `${env.fixturePrefix} webhook ingress`,
  });
  expect(ingress.status()).toBe(200);
  const ingressBody = (await ingress.json()) as {
    session_id: string;
    output: string;
  };
  expect(ingressBody.session_id).toBeTruthy();
  expect(ingressBody.output).toBe("release browser reply");

  const repeatedIngress = await page.request.post(`/webhooks/${channelID}?wait=true`, {
    headers: { Authorization: `Bearer ${pat.token}` },
    data: `${env.fixturePrefix} webhook ingress repeated`,
  });
  expect(repeatedIngress.status()).toBe(200);
  const repeatedBody = (await repeatedIngress.json()) as {
    session_id: string;
    output: string;
  };
  expect(repeatedBody.session_id).toBe(ingressBody.session_id);
  expect(repeatedBody.output).toBe("release browser reply");

  await requestJSON(page, "DELETE", `/api/users/me/tokens/${pat.personal_access_token.id}`);
  await page.getByRole("button", { name: "Delete", exact: true }).click();
  const deleteDialog = page.getByRole("dialog");
  const channelDeleted = page.waitForResponse(
    (response) =>
      response.url().endsWith(`/api/channels/${channelID}`) &&
      response.request().method() === "DELETE",
  );
  await deleteDialog.getByRole("button", { name: "Delete", exact: true }).click();
  expect((await channelDeleted).status()).toBe(204);
});

function requiredEnv(name: string): string {
  const value = process.env[name];
  if (!value) {
    throw new Error(`${name} is required`);
  }
  return value;
}

function redactEphemeralValues(value: string): string {
  const ephemeralValues = [
    env.adminPassword,
    env.userPassword,
    env.signupPassword,
    env.secretProbe,
  ].filter((candidate) => candidate.length >= 4);
  return ephemeralValues.reduce(
    (redacted, candidate) => redacted.replaceAll(candidate, "[REDACTED]"),
    value,
  );
}

function memorySection(page: Page, title: string): Locator {
  return page
    .locator('[data-slot="collapsible"]')
    .filter({
      // Count badges and descriptions are part of the trigger's accessible
      // name, so match the stable title prefix instead of mutable full text.
      has: page.getByRole("button", {
        name: new RegExp(`^${escapeRegExp(title)}`),
      }),
    })
    .first();
}

async function fillLogin(page: Page, email: string, password: string): Promise<void> {
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
}

async function login(page: Page, email: string, password: string): Promise<void> {
  await page.goto("/login");
  await fillLogin(page, email, password);
  await expect(page).not.toHaveURL(/\/login(?:[/?#]|$)/);
}

async function logout(page: Page): Promise<void> {
  // The responsive shell places the account menu in the sidebar footer, not
  // inside the page header. The avatar identifies the account menu trigger.
  await page.locator('[data-slot="menu-trigger"]:has([data-slot="avatar"])').click();
  await page.getByRole("menuitem", { name: "Log out", exact: true }).click();
  await expect(page).toHaveURL(/\/login(?:[/?#]|$)/);
}

function selectNearLabel(page: Page, label: string): Locator {
  return page
    .locator("label")
    .filter({ hasText: new RegExp(`^${escapeRegExp(label)}$`) })
    .locator("..")
    .locator("select");
}

function inputNearLabel(page: Page, label: string): Locator {
  return page
    .locator("label")
    .filter({ hasText: new RegExp(`^${escapeRegExp(label)}$`) })
    .locator("..")
    .locator("input");
}

function isProviderModelFetch(providerID: string): (response: Response) => boolean {
  return (response) =>
    response.url().endsWith(`/api/providers/${providerID}/models`) &&
    response.request().method() === "POST";
}

async function saveChannel(page: Page, channelID: string): Promise<void> {
  const response = page.waitForResponse(
    (candidate) =>
      candidate.url().endsWith(`/api/channels/${channelID}`) &&
      candidate.request().method() === "PATCH",
  );
  await page.getByRole("button", { name: "Save", exact: true }).click();
  expect((await response).ok()).toBeTruthy();
  await expect(page.getByText(`${channelID} saved`, { exact: true })).toBeVisible();
}

async function createModelFixture(
  page: Page,
  suffix: string,
): Promise<{ providerID: string; agentID: string }> {
  const providerID = `${env.fixturePrefix}-${suffix}-provider`;
  await requestJSON(page, "POST", "/api/providers", {
    id: providerID,
    type: "anthropic",
    name: `Release Browser ${suffix}`,
    enabled: true,
    api_key: `${env.fixturePrefix}-fake-key`,
    base_url: env.fakeProviderURL,
    models: {},
  });
  const agent = await requestJSON<{ id: string }>(page, "POST", "/api/agents", {
    name: `${env.fixturePrefix}-${suffix}-agent`,
    model: `${providerID}/claude-release-browser`,
    enabled: true,
  });
  return { providerID, agentID: agent.id };
}

async function requestJSON<T = unknown>(
  page: Page,
  method: string,
  path: string,
  data?: unknown,
): Promise<T> {
  const response = await page.request.fetch(path, { method, data });
  expect(response.ok(), `${method} ${new URL(response.url()).pathname}`).toBeTruthy();
  if (response.status() === 204) {
    return undefined as T;
  }
  return jsonFrom<T>(response);
}

async function jsonFrom<T>(response: Pick<APIResponse, "json" | "ok">): Promise<T> {
  expect(response.ok()).toBeTruthy();
  return (await response.json()) as T;
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
