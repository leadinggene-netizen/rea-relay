/**
 * POST /api/billing/checkout — start a hosted Stripe Checkout session.
 *
 * Reads the chosen plan `tier` (form-encoded or JSON) for the signed-in user,
 * resolves the tier's Stripe price id, reuses the tenant's Stripe customer when
 * known, creates a Checkout Session in `subscription` mode, and redirects (303)
 * to the hosted Checkout URL. The Subscription row is written by the webhook,
 * not here.
 */
import { NextResponse, type NextRequest } from "next/server";
import { auth } from "@/lib/auth";
import { getStripe, isStripeConfigured } from "@/lib/stripe";
import { buildCheckoutSessionParams } from "@/lib/billing";
import { DrizzleSubscriptionStore } from "@/lib/billing-store";
import { getPlan } from "@/lib/plans";
import { ensureTenantForUser } from "@/lib/tenant";

// Never prerendered; talks to Stripe per request.
export const runtime = "nodejs";
export const dynamic = "force-dynamic";

async function readTier(req: NextRequest): Promise<string | null> {
  const contentType = req.headers.get("content-type") ?? "";
  if (contentType.includes("application/json")) {
    const body = (await req.json().catch(() => null)) as {
      tier?: string;
    } | null;
    return body?.tier ?? null;
  }
  const form = await req.formData().catch(() => null);
  const tier = form?.get("tier");
  return typeof tier === "string" ? tier : null;
}

function baseUrlFrom(req: NextRequest): string {
  return (
    process.env.AUTH_URL ??
    process.env.NEXT_PUBLIC_APP_URL ??
    req.nextUrl.origin
  );
}

export async function POST(req: NextRequest) {
  if (!isStripeConfigured()) {
    return NextResponse.json(
      { error: "stripe_not_configured" },
      { status: 503 },
    );
  }

  const session = await auth();
  const email = session?.user?.email;
  if (!email) {
    return NextResponse.json({ error: "unauthenticated" }, { status: 401 });
  }

  // Resolve (or lazily create) the real tenant for this user, so the Stripe
  // metadata carries a tenant id that genuinely exists — the webhook's FK then
  // resolves against a real Tenant row (no dev shim needed).
  const { tenantId } = await ensureTenantForUser(
    email,
    session.user?.name ?? undefined,
  );

  const rawTier = await readTier(req);
  const plan = rawTier ? getPlan(rawTier) : undefined;
  if (!plan) {
    return NextResponse.json({ error: "invalid_tier" }, { status: 400 });
  }

  let customerId: string | null = null;
  try {
    customerId = await new DrizzleSubscriptionStore().stripeCustomerIdForTenant(
      tenantId,
    );
  } catch {
    // No DB available (e.g. not provisioned yet) — fall back to email/customer
    // creation inside Checkout. Not fatal for starting a session.
    customerId = null;
  }

  const stripe = getStripe();
  const params = buildCheckoutSessionParams({
    tier: plan.tier,
    tenantId,
    baseUrl: baseUrlFrom(req),
    stripeCustomerId: customerId,
    customerEmail: session?.user?.email ?? undefined,
  });

  const checkout = await stripe.checkout.sessions.create(params);
  if (!checkout.url) {
    return NextResponse.json(
      { error: "checkout_session_no_url" },
      { status: 502 },
    );
  }

  // 303 so the browser issues a GET to the hosted Checkout page.
  return NextResponse.redirect(checkout.url, 303);
}
