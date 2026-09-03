import postgres from "postgres";
import type { TestbedCredentials } from "./testbed.ts";

export type Sql = ReturnType<typeof postgres>;

// Direct connection to the testbed's embedded PostgreSQL so specs can assert
// on persisted rows, not just API echoes.
export function connectDB(creds: TestbedCredentials): Sql {
  if (!creds.database_url) {
    throw new Error("testbed credentials carry no database_url; rebuild dist/bin/testbed from this checkout");
  }
  return postgres(creds.database_url, { max: 2, onnotice: () => {} });
}
