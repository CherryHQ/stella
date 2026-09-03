import { expect, type Page, test as base } from "@playwright/test";
import { ApiClient } from "./api.ts";
import { connectDB, type Sql } from "./db.ts";
import { loadCredentials, loadTestbedState, type TestbedCredentials } from "./testbed.ts";

export interface E2EFixtures {
  creds: TestbedCredentials;
  admin: ApiClient;
  user: ApiClient;
  db: Sql;
  loginAsAdmin: () => Promise<void>;
}

export const test = base.extend<E2EFixtures>({
  baseURL: async ({}, use) => {
    await use(loadTestbedState().baseURL);
  },
  creds: async ({}, use) => {
    await use(loadCredentials(loadTestbedState()));
  },
  admin: async ({ creds }, use) => {
    await use(new ApiClient(creds.base_url, creds.admin.token));
  },
  user: async ({ creds }, use) => {
    await use(new ApiClient(creds.base_url, creds.user.token));
  },
  db: async ({ creds }, use) => {
    const sql = connectDB(creds);
    await use(sql);
    await sql.end({ timeout: 5 });
  },
  loginAsAdmin: async ({ page, creds }, use) => {
    await use(async () => loginWithPassword(page, creds.admin.email, creds.admin.password));
  },
});

export { expect };

// Drives the real login form so the browser holds the same session cookie a
// human would; PATs never touch the UI.
export async function loginWithPassword(page: Page, email: string, password: string): Promise<void> {
  await page.goto("/login");
  await page.locator("#email").fill(email);
  await page.locator("#password").fill(password);
  await page.getByRole("button", { name: /sign in/i }).click();
  await page.waitForURL((url) => !url.pathname.startsWith("/login"));
}
