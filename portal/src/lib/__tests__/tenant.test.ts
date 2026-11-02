/**
 * Unit tests for ensureTenantForUser — fully offline with a mocked Drizzle
 * client. Exercises the existing-user fast path and the new-user transactional
 * create path.
 */
import { describe, it, expect } from "vitest";
import { ensureTenantForUser } from "@/lib/tenant";
import { tenants, users } from "@/db/schema";

/**
 * Minimal fake Drizzle client. `select(...).from(users)` returns the scripted
 * existing user (or none); inserts into tenants/users record their values and
 * return a scripted id via `.returning()`. `transaction(cb)` just runs `cb`
 * against the same fake (no real isolation needed for the test).
 */
function fakeDb(opts: {
  existingUser: { id: string; tenantId: string } | null;
  inserts: Array<{ table: "tenants" | "users"; values: Record<string, unknown> }>;
  newTenantId?: string;
  newUserId?: string;
}) {
  const { existingUser, inserts } = opts;
  const newTenantId = opts.newTenantId ?? "tenant-new";
  const newUserId = opts.newUserId ?? "user-new";

  const client = {
    select() {
      let table: unknown;
      const builder = {
        from(t: unknown) {
          table = t;
          return builder;
        },
        where() {
          return builder;
        },
        limit() {
          if (table === users) {
            return Promise.resolve(existingUser ? [existingUser] : []);
          }
          return Promise.resolve([]);
        },
      };
      return builder;
    },
    insert(table: unknown) {
      return {
        values(values: Record<string, unknown>) {
          const name = table === tenants ? "tenants" : "users";
          inserts.push({ table: name, values });
          return {
            returning() {
              return Promise.resolve([
                { id: table === tenants ? newTenantId : newUserId },
              ]);
            },
          };
        },
      };
    },
    transaction(cb: (tx: unknown) => Promise<unknown>) {
      return cb(client);
    },
  };
  return client;
}

describe("ensureTenantForUser", () => {
  it("returns the existing tenant/user without inserting (idempotent)", async () => {
    const inserts: Array<{ table: "tenants" | "users"; values: Record<string, unknown> }> =
      [];
    const db = fakeDb({
      existingUser: { id: "user-42", tenantId: "tenant-7" },
      inserts,
    });

    const result = await ensureTenantForUser(
      "xin@example.com",
      "Xin",
      db as never,
    );

    expect(result).toEqual({ tenantId: "tenant-7", userId: "user-42" });
    expect(inserts).toHaveLength(0);
  });

  it("creates a tenant + OWNER user for a brand-new email", async () => {
    const inserts: Array<{ table: "tenants" | "users"; values: Record<string, unknown> }> =
      [];
    const db = fakeDb({
      existingUser: null,
      inserts,
      newTenantId: "tenant-abc",
      newUserId: "user-xyz",
    });

    const result = await ensureTenantForUser(
      "new@example.com",
      "New Person",
      db as never,
    );

    expect(result).toEqual({ tenantId: "tenant-abc", userId: "user-xyz" });
    expect(inserts).toHaveLength(2);

    const tenantInsert = inserts.find((i) => i.table === "tenants");
    const userInsert = inserts.find((i) => i.table === "users");
    expect(tenantInsert?.values).toMatchObject({ name: "New Person" });
    expect(userInsert?.values).toMatchObject({
      email: "new@example.com",
      name: "New Person",
      role: "OWNER",
      tenantId: "tenant-abc",
    });
  });

  it("falls back to the email as the tenant name when no name is given", async () => {
    const inserts: Array<{ table: "tenants" | "users"; values: Record<string, unknown> }> =
      [];
    const db = fakeDb({ existingUser: null, inserts });

    await ensureTenantForUser("solo@example.com", undefined, db as never);

    const tenantInsert = inserts.find((i) => i.table === "tenants");
    const userInsert = inserts.find((i) => i.table === "users");
    expect(tenantInsert?.values).toMatchObject({ name: "solo@example.com" });
    expect(userInsert?.values).toMatchObject({ name: null });
  });
});
