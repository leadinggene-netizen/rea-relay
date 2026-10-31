import { describe, it, expect, vi } from "vitest";
import {
  priceIdForTier,
  tierForPriceId,
  mapStripeSubscriptionStatus,
  buildCheckoutSessionParams,
  handleStripeWebhookEvent,
  type SubscriptionStore,
  type SubscriptionUpsert,
} from "@/lib/billing";
import { DrizzleSubscriptionStore } from "@/lib/billing-store";
import { plans, subscriptions } from "@/db/schema";
import type Stripe from "stripe";

const PRICE_ENV = {
  STRIPE_PRICE_STARTER: "price_starter_123",
  STRIPE_PRICE_ADVANCED: "price_advanced_456",
  STRIPE_PRICE_PRO: "price_pro_789",
} as unknown as NodeJS.ProcessEnv;

// --- tier <-> price id -------------------------------------------------------

describe("priceIdForTier", () => {
  it("resolves each tier to its configured env price id", () => {
    expect(priceIdForTier("STARTER", PRICE_ENV)).toBe("price_starter_123");
    expect(priceIdForTier("ADVANCED", PRICE_ENV)).toBe("price_advanced_456");
    expect(priceIdForTier("PRO", PRICE_ENV)).toBe("price_pro_789");
  });

  it("throws a clear error when the price id is not configured", () => {
    expect(() => priceIdForTier("PRO", {} as NodeJS.ProcessEnv)).toThrow(
      /STRIPE_PRICE_PRO/,
    );
  });
});

describe("tierForPriceId", () => {
  it("reverse-maps a configured price id back to its tier", () => {
    expect(tierForPriceId("price_advanced_456", PRICE_ENV)).toBe("ADVANCED");
  });
  it("returns undefined for unknown or empty price ids", () => {
    expect(tierForPriceId("price_unknown", PRICE_ENV)).toBeUndefined();
    expect(tierForPriceId(null, PRICE_ENV)).toBeUndefined();
  });
});

// --- status mapping ----------------------------------------------------------

describe("mapStripeSubscriptionStatus", () => {
  it("maps live statuses to ACTIVE", () => {
    expect(mapStripeSubscriptionStatus("active")).toBe("ACTIVE");
    expect(mapStripeSubscriptionStatus("trialing")).toBe("ACTIVE");
  });
  it("maps payment-trouble statuses to PAST_DUE", () => {
    expect(mapStripeSubscriptionStatus("past_due")).toBe("PAST_DUE");
    expect(mapStripeSubscriptionStatus("unpaid")).toBe("PAST_DUE");
  });
  it("maps terminal statuses to CANCELED", () => {
    expect(mapStripeSubscriptionStatus("canceled")).toBe("CANCELED");
    expect(mapStripeSubscriptionStatus("incomplete_expired")).toBe("CANCELED");
  });
  it("maps not-yet-usable / unknown statuses to INCOMPLETE", () => {
    expect(mapStripeSubscriptionStatus("incomplete")).toBe("INCOMPLETE");
    expect(mapStripeSubscriptionStatus("paused")).toBe("INCOMPLETE");
    expect(mapStripeSubscriptionStatus("something_new")).toBe("INCOMPLETE");
  });
});

// --- checkout params ---------------------------------------------------------

describe("buildCheckoutSessionParams", () => {
  it("builds a subscription-mode session with tenant+tier metadata", () => {
    const params = buildCheckoutSessionParams({
      tier: "ADVANCED",
      tenantId: "tenant-1",
      baseUrl: "https://portal.example.com/",
      customerEmail: "x@example.com",
      env: PRICE_ENV,
    });
    expect(params.mode).toBe("subscription");
    expect(params.line_items).toEqual([
      { price: "price_advanced_456", quantity: 1 },
    ]);
    expect(params.client_reference_id).toBe("tenant-1");
    expect(params.metadata).toEqual({ tenantId: "tenant-1", tier: "ADVANCED" });
    expect(params.subscription_data?.metadata).toEqual({
      tenantId: "tenant-1",
      tier: "ADVANCED",
    });
    // trailing slash normalized; no double slash
    expect(params.success_url).toBe(
      "https://portal.example.com/dashboard?checkout=success&session_id={CHECKOUT_SESSION_ID}",
    );
    expect(params.customer_email).toBe("x@example.com");
    expect(params.customer).toBeUndefined();
  });

  it("reuses an existing Stripe customer instead of email when known", () => {
    const params = buildCheckoutSessionParams({
      tier: "STARTER",
      tenantId: "tenant-2",
      baseUrl: "https://portal.example.com",
      stripeCustomerId: "cus_existing",
      customerEmail: "ignored@example.com",
      env: PRICE_ENV,
    });
    expect(params.customer).toBe("cus_existing");
    expect(params.customer_email).toBeUndefined();
  });
});

// --- webhook handler: dispatch + activation-once ----------------------------

/** Minimal fake store that records calls and returns a scripted activation. */
function fakeStore(activated: boolean): {
  store: SubscriptionStore;
  calls: SubscriptionUpsert[];
} {
  const calls: SubscriptionUpsert[] = [];
  return {
    calls,
    store: {
      async upsertSubscription(data) {
        calls.push(data);
        return { activated };
      },
    },
  };
}

function subscriptionEvent(
  type:
    | "customer.subscription.created"
    | "customer.subscription.updated"
    | "customer.subscription.deleted",
  overrides: Partial<Stripe.Subscription> = {},
): Stripe.Event {
  const sub = {
    id: "sub_123",
    object: "subscription",
    status: "active",
    customer: "cus_123",
    metadata: { tenantId: "tenant-1", tier: "PRO" },
    items: {
      data: [
        { price: { id: "price_pro_789" }, current_period_end: 1893456000 },
      ],
    },
    ...overrides,
  } as unknown as Stripe.Subscription;
  return {
    id: "evt_1",
    type,
    data: { object: sub },
  } as unknown as Stripe.Event;
}

describe("handleStripeWebhookEvent", () => {
  it("upserts the mapped subscription fields for a subscription event", async () => {
    const { store, calls } = fakeStore(false);
    const res = await handleStripeWebhookEvent(
      subscriptionEvent("customer.subscription.updated"),
      { store },
      PRICE_ENV,
    );
    expect(res.handled).toBe(true);
    expect(calls).toHaveLength(1);
    expect(calls[0]).toMatchObject({
      tenantId: "tenant-1",
      tier: "PRO",
      status: "ACTIVE",
      stripeSubscriptionId: "sub_123",
      stripeCustomerId: "cus_123",
    });
    expect(calls[0].currentPeriodEnd).toBeInstanceOf(Date);
  });

  it("derives the tier from the price id when metadata.tier is absent", async () => {
    const { store, calls } = fakeStore(false);
    await handleStripeWebhookEvent(
      subscriptionEvent("customer.subscription.created", {
        metadata: { tenantId: "tenant-1" },
      } as Partial<Stripe.Subscription>),
      { store },
      PRICE_ENV,
    );
    expect(calls[0].tier).toBe("PRO");
  });

  it("invokes onSubscriptionActivated exactly once when it goes active", async () => {
    const { store } = fakeStore(true); // store reports the transition into ACTIVE
    const onActivated = vi.fn();
    await handleStripeWebhookEvent(
      subscriptionEvent("customer.subscription.updated"),
      { store, onActivated },
      PRICE_ENV,
    );
    expect(onActivated).toHaveBeenCalledTimes(1);
    expect(onActivated).toHaveBeenCalledWith("tenant-1", "sub_123");
  });

  it("does NOT invoke onSubscriptionActivated when there is no transition", async () => {
    const { store } = fakeStore(false); // already active / not a transition
    const onActivated = vi.fn();
    await handleStripeWebhookEvent(
      subscriptionEvent("customer.subscription.updated"),
      { store, onActivated },
      PRICE_ENV,
    );
    expect(onActivated).not.toHaveBeenCalled();
  });

  it("handles checkout.session.completed as ACTIVE when paid", async () => {
    const { store, calls } = fakeStore(true);
    const onActivated = vi.fn();
    const event = {
      id: "evt_2",
      type: "checkout.session.completed",
      data: {
        object: {
          id: "cs_1",
          object: "checkout.session",
          status: "complete",
          payment_status: "paid",
          client_reference_id: "tenant-9",
          metadata: { tenantId: "tenant-9", tier: "STARTER" },
          customer: "cus_9",
          subscription: "sub_9",
        },
      },
    } as unknown as Stripe.Event;
    const res = await handleStripeWebhookEvent(event, { store, onActivated });
    expect(res.handled).toBe(true);
    expect(calls[0]).toMatchObject({
      tenantId: "tenant-9",
      tier: "STARTER",
      status: "ACTIVE",
      stripeCustomerId: "cus_9",
      stripeSubscriptionId: "sub_9",
    });
    expect(onActivated).toHaveBeenCalledTimes(1);
  });

  it("ignores unrelated event types", async () => {
    const { store, calls } = fakeStore(true);
    const res = await handleStripeWebhookEvent(
      { id: "evt_x", type: "invoice.paid", data: { object: {} } } as unknown as Stripe.Event,
      { store },
      PRICE_ENV,
    );
    expect(res.handled).toBe(false);
    expect(calls).toHaveLength(0);
  });

  it("ignores subscription events without our tenant metadata", async () => {
    const { store, calls } = fakeStore(false);
    const res = await handleStripeWebhookEvent(
      subscriptionEvent("customer.subscription.updated", {
        metadata: {},
      } as Partial<Stripe.Subscription>),
      { store },
      PRICE_ENV,
    );
    expect(res.handled).toBe(false);
    expect(calls).toHaveLength(0);
  });
});

// --- Drizzle store: activation transition computed from prior row -----------

type WriteOp = { op: "insert" | "update"; values: Record<string, unknown> };

function fakeDb(opts: {
  plan: { id: string } | null;
  existing: { id: string; status: string } | null;
  writes: WriteOp[];
}) {
  const { plan, existing, writes } = opts;
  return {
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
          if (table === plans) return Promise.resolve(plan ? [plan] : []);
          if (table === subscriptions)
            return Promise.resolve(existing ? [existing] : []);
          return Promise.resolve([]);
        },
      };
      return builder;
    },
    update() {
      const builder = {
        set(values: Record<string, unknown>) {
          writes.push({ op: "update", values });
          return builder;
        },
        where() {
          return Promise.resolve();
        },
      };
      return builder;
    },
    insert() {
      return {
        values(values: Record<string, unknown>) {
          writes.push({ op: "insert", values });
          return Promise.resolve();
        },
      };
    },
  };
}

const UPSERT: SubscriptionUpsert = {
  tenantId: "tenant-1",
  tier: "PRO",
  status: "ACTIVE",
  stripeSubscriptionId: "sub_123",
  stripeCustomerId: "cus_123",
  currentPeriodEnd: new Date("2030-01-01T00:00:00Z"),
};

describe("DrizzleSubscriptionStore.upsertSubscription", () => {
  it("inserts a new row and reports activation when first ACTIVE", async () => {
    const writes: WriteOp[] = [];
    const db = fakeDb({ plan: { id: "plan_pro" }, existing: null, writes });
    const store = new DrizzleSubscriptionStore(db as never);
    const { activated } = await store.upsertSubscription(UPSERT);
    expect(activated).toBe(true);
    expect(writes).toHaveLength(1);
    expect(writes[0].op).toBe("insert");
    expect(writes[0].values).toMatchObject({
      tenantId: "tenant-1",
      planId: "plan_pro",
      status: "ACTIVE",
      stripeCustomerId: "cus_123",
    });
  });

  it("updates an existing row; already-ACTIVE is NOT a new activation", async () => {
    const writes: WriteOp[] = [];
    const db = fakeDb({
      plan: { id: "plan_pro" },
      existing: { id: "sub_row", status: "ACTIVE" },
      writes,
    });
    const store = new DrizzleSubscriptionStore(db as never);
    const { activated } = await store.upsertSubscription(UPSERT);
    expect(activated).toBe(false);
    expect(writes[0].op).toBe("update");
  });

  it("updates an existing non-active row and reports activation on transition", async () => {
    const writes: WriteOp[] = [];
    const db = fakeDb({
      plan: { id: "plan_pro" },
      existing: { id: "sub_row", status: "INCOMPLETE" },
      writes,
    });
    const store = new DrizzleSubscriptionStore(db as never);
    const { activated } = await store.upsertSubscription(UPSERT);
    expect(activated).toBe(true);
    expect(writes[0].op).toBe("update");
  });

  it("throws if the plan tier has not been seeded", async () => {
    const writes: WriteOp[] = [];
    const db = fakeDb({ plan: null, existing: null, writes });
    const store = new DrizzleSubscriptionStore(db as never);
    await expect(store.upsertSubscription(UPSERT)).rejects.toThrow(/Plan/);
  });
});
