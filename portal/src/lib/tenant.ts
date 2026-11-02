/**
 * Tenant/user persistence for the portal.
 *
 * `ensureTenantForUser` is the single entry point that maps a signed-in
 * identity (email) to a real, persisted `{tenantId, userId}` pair. Auth.js uses
 * JWT sessions with no DB adapter, so the User/Tenant rows are created lazily
 * the first time a user does something that needs a real tenant id (checkout,
 * provisioning, adding connections).
 *
 * Model decision (see spec): ONE tenant per user to start. The schema supports
 * tenant→many users, but this helper always creates a fresh tenant for a brand
 * new user and never attaches a user to an existing tenant.
 *
 * Server-only: the `db` client is lazy (see src/db/index.ts), so importing this
 * module opens no socket; a database is only required when the function runs.
 */
import { eq } from "drizzle-orm";
import { db } from "@/db";
import { tenants, users } from "@/db/schema";

type DbLike = typeof db;
/** The transaction handle passed to `db.transaction(cb)` — same query surface. */
type Tx = Parameters<Parameters<DbLike["transaction"]>[0]>[0];

export interface EnsureTenantResult {
  tenantId: string;
  userId: string;
}

/**
 * Resolve (or create) the tenant + user for an email. Idempotent:
 * - If a User with this email exists, return its `{tenantId, userId}`.
 * - Otherwise create a Tenant (name = `name ?? email`) and an OWNER User in a
 *   single transaction, and return the new ids.
 *
 * `client` is injectable for tests (defaults to the shared lazy `db`).
 */
export async function ensureTenantForUser(
  email: string,
  name?: string,
  client: DbLike = db,
): Promise<EnsureTenantResult> {
  const existing = await findUserByEmail(client, email);
  if (existing) {
    return { tenantId: existing.tenantId, userId: existing.id };
  }

  // Create the tenant + owner user atomically so we never leave a dangling
  // tenant if the user insert fails.
  return client.transaction(async (tx) => {
    // Re-check inside the transaction to stay idempotent under a concurrent
    // first-login race (the email unique constraint is the ultimate backstop).
    const raced = await findUserByEmail(tx, email);
    if (raced) {
      return { tenantId: raced.tenantId, userId: raced.id };
    }

    const [tenant] = await tx
      .insert(tenants)
      .values({ name: name ?? email })
      .returning({ id: tenants.id });

    const [user] = await tx
      .insert(users)
      .values({
        email,
        name: name ?? null,
        role: "OWNER",
        tenantId: tenant.id,
      })
      .returning({ id: users.id });

    return { tenantId: tenant.id, userId: user.id };
  });
}

async function findUserByEmail(
  client: DbLike | Tx,
  email: string,
): Promise<{ id: string; tenantId: string } | undefined> {
  const [row] = await client
    .select({ id: users.id, tenantId: users.tenantId })
    .from(users)
    .where(eq(users.email, email))
    .limit(1);
  return row;
}
