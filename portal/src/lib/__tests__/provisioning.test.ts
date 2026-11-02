/**
 * Unit tests for provisionOnActivation — fully offline. The relay client is
 * mocked (so no network), the DB is a tiny fake, and RELAY_ADMIN_* env is
 * toggled per test. Covers: not-configured, happy path, already-linked.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// Mock the relay client module so no fetch/network happens.
const createSubscription = vi.fn();
vi.mock("@/lib/relay", () => ({
  relayClientFromEnv: () => ({ createSubscription }),
}));

import { provisionOnActivation } from "@/lib/provisioning";

interface SubRow {
  id: string;
  relaySubscriptionId: string | null;
  tier: string;
  maxConnections: number;
}

type Update = { relaySubscriptionId?: string | null };

/**
 * Fake Drizzle client for the join-select + update provisioning does.
 * `select().from().innerJoin().where().limit()` resolves the scripted row;
 * `update().set().where()` records the patch.
 */
function fakeDb(row: SubRow | null, updates: Update[]) {
  return {
    select() {
      const builder = {
        from() {
          return builder;
        },
        innerJoin() {
          return builder;
        },
        where() {
          return builder;
        },
        limit() {
          return Promise.resolve(row ? [row] : []);
        },
      };
      return builder;
    },
    update() {
      return {
        set(values: Update) {
          updates.push(values);
          return {
            where() {
              return Promise.resolve();
            },
          };
        },
      };
    },
  };
}

describe("provisionOnActivation", () => {
  beforeEach(() => {
    createSubscription.mockReset();
    createSubscription.mockResolvedValue({ id: "ok" });
  });

  afterEach(() => {
    delete process.env.RELAY_ADMIN_URL;
    delete process.env.RELAY_ADMIN_TOKEN;
  });

  it("no-ops when the relay is not configured", async () => {
    const updates: Update[] = [];
    const db = fakeDb(
      { id: "sub-1", relaySubscriptionId: null, tier: "PRO", maxConnections: 5 },
      updates,
    );
    await provisionOnActivation("tenant-1", "stripe_sub_1", db as never);
    expect(createSubscription).not.toHaveBeenCalled();
    expect(updates).toHaveLength(0);
  });

  it("creates the relay subscription and persists the link (happy path)", async () => {
    process.env.RELAY_ADMIN_URL = "http://relay.local:8080";
    process.env.RELAY_ADMIN_TOKEN = "token";
    const updates: Update[] = [];
    const db = fakeDb(
      { id: "sub-1", relaySubscriptionId: null, tier: "PRO", maxConnections: 5 },
      updates,
    );

    await provisionOnActivation("tenant-1", "stripe_sub_1", db as never);

    expect(createSubscription).toHaveBeenCalledTimes(1);
    expect(createSubscription).toHaveBeenCalledWith({
      id: "sub-1",
      tier: "PRO",
      max_connections: 5,
      owner_id: "tenant-1",
    });
    expect(updates).toEqual([{ relaySubscriptionId: "sub-1" }]);
  });

  it("is a no-op when already linked (idempotent)", async () => {
    process.env.RELAY_ADMIN_URL = "http://relay.local:8080";
    process.env.RELAY_ADMIN_TOKEN = "token";
    const updates: Update[] = [];
    const db = fakeDb(
      {
        id: "sub-1",
        relaySubscriptionId: "sub-1",
        tier: "PRO",
        maxConnections: 5,
      },
      updates,
    );

    await provisionOnActivation("tenant-1", "stripe_sub_1", db as never);

    expect(createSubscription).not.toHaveBeenCalled();
    expect(updates).toHaveLength(0);
  });

  it("no-ops when the tenant has no subscription row", async () => {
    process.env.RELAY_ADMIN_URL = "http://relay.local:8080";
    process.env.RELAY_ADMIN_TOKEN = "token";
    const updates: Update[] = [];
    const db = fakeDb(null, updates);

    await provisionOnActivation("tenant-1", "stripe_sub_1", db as never);

    expect(createSubscription).not.toHaveBeenCalled();
    expect(updates).toHaveLength(0);
  });

  it("swallows relay errors (never throws out of the hook)", async () => {
    process.env.RELAY_ADMIN_URL = "http://relay.local:8080";
    process.env.RELAY_ADMIN_TOKEN = "token";
    createSubscription.mockRejectedValue(new Error("relay down"));
    const updates: Update[] = [];
    const db = fakeDb(
      { id: "sub-1", relaySubscriptionId: null, tier: "PRO", maxConnections: 5 },
      updates,
    );

    await expect(
      provisionOnActivation("tenant-1", "stripe_sub_1", db as never),
    ).resolves.toBeUndefined();
    expect(updates).toHaveLength(0);
  });
});
