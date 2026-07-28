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
};

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
  const ephemeralValues = [env.adminPassword, env.userPassword, env.signupPassword].filter(
    (candidate) => candidate.length >= 4,
  );
  return ephemeralValues.reduce(
    (redacted, candidate) => redacted.replaceAll(candidate, "[REDACTED]"),
    value,
  );
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
