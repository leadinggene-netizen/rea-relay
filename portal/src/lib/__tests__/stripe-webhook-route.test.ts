/**
 * Route-level test for POST /api/stripe/webhook.
 *
 * Exercises real Stripe signature verification (constructEvent +
 * generateTestHeaderString) over the raw request body, fully offline. The
 * Drizzle-backed store is mocked so nothing touches a database.
 */
import { describe, it, expect, vi, beforeEach } from "vitest";

// Record upserts made by the (mocked) store so we can assert the DB was updated.
const upsertCalls: unknown[] = [];
vi.mock("@/lib/billing-store", () => ({
  DrizzleSubscriptionStore: class {
    async upsertSubscription(data: unknown) {
      upsertCalls.push(data);
      return { activated: true };
    }
    async stripeCustomerIdForTenant() {
      return null;
    }
  },
}));

import { NextRequest } from "next/server";
import { POST } from "@/app/api/stripe/webhook/route";
import { getStripe, __resetStripeForTests } from "@/lib/stripe";

const WEBHOOK_SECRET = "whsec_test_secret";

function makeRequest(body: string, signature: string | null): NextRequest {
  const headers = new Headers({ "content-type": "application/json" });
  if (signature !== null) headers.set("stripe-signature", signature);
  return new NextRequest("http://localhost:3000/api/stripe/webhook", {
    method: "POST",
    headers,
    body,
  });
}

function eventPayload(): string {
  return JSON.stringify({
    id: "evt_test",
    object: "event",
    type: "customer.subscription.updated",
    data: {
      object: {
        id: "sub_test",
        object: "subscription",
        status: "active",
        customer: "cus_test",
        metadata: { tenantId: "tenant-77", tier: "ADVANCED" },
        items: {
          data: [
            { price: { id: "price_advanced_456" }, current_period_end: 1893456000 },
          ],
        },
      },
    },
  });
}

describe("POST /api/stripe/webhook", () => {
  beforeEach(() => {
    upsertCalls.length = 0;
    __resetStripeForTests();
    process.env.STRIPE_SECRET_KEY = "sk_test_dummy";
    process.env.STRIPE_WEBHOOK_SECRET = WEBHOOK_SECRET;
    process.env.STRIPE_PRICE_ADVANCED = "price_advanced_456";
  });

  it("accepts a correctly-signed event and updates the (mocked) DB", async () => {
    const payload = eventPayload();
    const signature = getStripe().webhooks.generateTestHeaderString({
      payload,
      secret: WEBHOOK_SECRET,
    });

    const res = await POST(makeRequest(payload, signature));
    expect(res.status).toBe(200);
    const json = await res.json();
    expect(json).toEqual({ received: true, handled: true });

    expect(upsertCalls).toHaveLength(1);
    expect(upsertCalls[0]).toMatchObject({
      tenantId: "tenant-77",
      tier: "ADVANCED",
      status: "ACTIVE",
      stripeSubscriptionId: "sub_test",
      stripeCustomerId: "cus_test",
    });
  });

  it("rejects a bad signature with 400 and does not touch the DB", async () => {
    const payload = eventPayload();
    const res = await POST(makeRequest(payload, "t=1,v1=deadbeef"));
    expect(res.status).toBe(400);
    const json = await res.json();
    expect(json.error).toBe("signature_verification_failed");
    expect(upsertCalls).toHaveLength(0);
  });

  it("rejects a signature computed with the wrong secret (400)", async () => {
    const payload = eventPayload();
    const signature = getStripe().webhooks.generateTestHeaderString({
      payload,
      secret: "whsec_WRONG",
    });
    const res = await POST(makeRequest(payload, signature));
    expect(res.status).toBe(400);
    expect(upsertCalls).toHaveLength(0);
  });

  it("returns 400 when the signature header is missing", async () => {
    const res = await POST(makeRequest(eventPayload(), null));
    expect(res.status).toBe(400);
    const json = await res.json();
    expect(json.error).toBe("missing_signature");
  });

  it("returns 503 when Stripe is not configured", async () => {
    delete process.env.STRIPE_WEBHOOK_SECRET;
    const res = await POST(makeRequest(eventPayload(), "sig"));
    expect(res.status).toBe(503);
  });
});
