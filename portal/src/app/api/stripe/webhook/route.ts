/**
 * POST /api/stripe/webhook — receive and verify Stripe webhook events.
 *
 * Signature verification requires the EXACT raw request body, so we read
 * `await req.text()` (never req.json()) and hand it to
 * stripe.webhooks.constructEvent with STRIPE_WEBHOOK_SECRET. The route runs on
 * the Node.js runtime and is dynamic so Next.js never caches it or reparses the
 * body.
 *
 * Handled events upsert the tenant's Subscription row (idempotent). The
 * provisioning hook fires once on the transition to ACTIVE — see billing.ts.
 */
import { NextResponse, type NextRequest } from "next/server";
import type Stripe from "stripe";
import { getStripe, isStripeConfigured } from "@/lib/stripe";
import { handleStripeWebhookEvent } from "@/lib/billing";
import { DrizzleSubscriptionStore } from "@/lib/billing-store";
import { provisionOnActivation } from "@/lib/provisioning";

// Raw body + no caching: required for signature verification.
export const runtime = "nodejs";
export const dynamic = "force-dynamic";

export async function POST(req: NextRequest) {
  const webhookSecret = process.env.STRIPE_WEBHOOK_SECRET;
  if (!isStripeConfigured() || !webhookSecret) {
    return NextResponse.json(
      { error: "stripe_not_configured" },
      { status: 503 },
    );
  }

  const signature = req.headers.get("stripe-signature");
  if (!signature) {
    return NextResponse.json({ error: "missing_signature" }, { status: 400 });
  }

  // RAW body — do not use req.json(); the exact bytes are what's signed.
  const rawBody = await req.text();

  let event: Stripe.Event;
  try {
    event = getStripe().webhooks.constructEvent(
      rawBody,
      signature,
      webhookSecret,
    );
  } catch (err) {
    const message = err instanceof Error ? err.message : "invalid signature";
    return NextResponse.json(
      { error: "signature_verification_failed", message },
      { status: 400 },
    );
  }

  try {
    const { handled } = await handleStripeWebhookEvent(event, {
      store: new DrizzleSubscriptionStore(),
      // Fire-and-log: provisioning talks to the relay, which must not block or
      // fail the webhook's 200. provisionOnActivation catches and logs its own
      // errors, so a provisioning hiccup never makes Stripe retry the event.
      onActivated: (tenantId, subscriptionId) => {
        void provisionOnActivation(tenantId, subscriptionId);
      },
    });
    return NextResponse.json({ received: true, handled });
  } catch (err) {
    // Return 500 so Stripe retries a transient persistence failure.
    const message = err instanceof Error ? err.message : "handler error";
    return NextResponse.json(
      { error: "handler_error", message },
      { status: 500 },
    );
  }
}
