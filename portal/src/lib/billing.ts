/**
 * Billing logic for Stripe subscription Checkout (hosted) — pure, I/O-free, and
 * unit tested. The route handlers in src/app/api/** wire these functions to the
 * Stripe SDK, the request/response, and the Drizzle-backed store.
 *
 * Nothing here opens a socket or touches the DB directly: tier→price resolution
 * reads env, status mapping is pure, and the webhook handler takes an injected
 * store + callbacks so it runs fully offline in tests.
 */
import type Stripe from "stripe";
import type { Tier } from "./plans";
import type { SubscriptionStatus } from "@/db/schema";

// --- tier <-> Stripe price id -----------------------------------------------

/** Env var name holding the Stripe price id for each tier. */
const PRICE_ENV_BY_TIER: Record<Tier, string> = {
  STARTER: "STRIPE_PRICE_STARTER",
  ADVANCED: "STRIPE_PRICE_ADVANCED",
  PRO: "STRIPE_PRICE_PRO",
};

/**
 * Resolve the Stripe price id for a tier from the environment.
 * Throws if the tier is unknown or the price id is not configured.
 */
export function priceIdForTier(
  tier: Tier,
  env: NodeJS.ProcessEnv = process.env,
): string {
  const key = PRICE_ENV_BY_TIER[tier];
  if (!key) throw new Error(`Unknown plan tier: ${tier}`);
  const priceId = env[key];
  if (!priceId) {
    throw new Error(
      `${key} is not set — create the ${tier} price in Stripe and configure ${key}`,
    );
  }
  return priceId;
}

/**
 * Reverse lookup: which tier a Stripe price id belongs to, by matching the
 * configured STRIPE_PRICE_* env vars. Returns undefined if none match.
 */
export function tierForPriceId(
  priceId: string | null | undefined,
  env: NodeJS.ProcessEnv = process.env,
): Tier | undefined {
  if (!priceId) return undefined;
  for (const tier of Object.keys(PRICE_ENV_BY_TIER) as Tier[]) {
    if (env[PRICE_ENV_BY_TIER[tier]] === priceId) return tier;
  }
  return undefined;
}

// --- Stripe status -> our enum ----------------------------------------------

/**
 * Map a Stripe subscription status to our `subscription_status` enum
 * (INCOMPLETE | ACTIVE | PAST_DUE | CANCELED).
 *
 * - active / trialing            -> ACTIVE  (trial is a live, usable sub)
 * - past_due / unpaid            -> PAST_DUE
 * - canceled / incomplete_expired-> CANCELED
 * - incomplete / paused / other  -> INCOMPLETE (not yet usable)
 */
export function mapStripeSubscriptionStatus(
  status: Stripe.Subscription.Status | string,
): SubscriptionStatus {
  switch (status) {
    case "active":
    case "trialing":
      return "ACTIVE";
    case "past_due":
    case "unpaid":
      return "PAST_DUE";
    case "canceled":
    case "incomplete_expired":
      return "CANCELED";
    case "incomplete":
    case "paused":
    default:
      return "INCOMPLETE";
  }
}

// --- provisioning hook (STUB) -----------------------------------------------

/**
 * Called exactly once when a tenant's subscription first becomes ACTIVE.
 *
 * STUB: the NEXT slice implements provisioning. This must NOT create relay
 * connections or call the relay here.
 */
export function onSubscriptionActivated(
  tenantId: string,
  subscriptionId: string,
): void {
  // TODO(provisioning): create relay connections + hosted MT5 cell for this
  // tenant/subscription (next slice). Keep this a no-op until then.
  console.info(
    `[billing] subscription activated (provisioning deferred): tenant=${tenantId} subscription=${subscriptionId}`,
  );
}

// --- Checkout session params ------------------------------------------------

export interface CheckoutSessionInput {
  tier: Tier;
  tenantId: string;
  /** Base URL of the portal, e.g. https://portal.example.com (no trailing slash needed). */
  baseUrl: string;
  /** Reuse an existing Stripe customer when known; otherwise fall back to email. */
  stripeCustomerId?: string | null;
  customerEmail?: string | null;
  env?: NodeJS.ProcessEnv;
}

/**
 * Build the params for a hosted Checkout Session in `subscription` mode.
 * tenant + tier are stamped into metadata (and the subscription's metadata) so
 * the webhook can attribute events without a reverse customer lookup.
 */
export function buildCheckoutSessionParams(
  input: CheckoutSessionInput,
): Stripe.Checkout.SessionCreateParams {
  const env = input.env ?? process.env;
  const base = input.baseUrl.replace(/\/+$/, "");
  const priceId = priceIdForTier(input.tier, env);
  const metadata = { tenantId: input.tenantId, tier: input.tier };

  const params: Stripe.Checkout.SessionCreateParams = {
    mode: "subscription",
    line_items: [{ price: priceId, quantity: 1 }],
    // Return to the dashboard either way; {CHECKOUT_SESSION_ID} is expanded by Stripe.
    success_url: `${base}/dashboard?checkout=success&session_id={CHECKOUT_SESSION_ID}`,
    cancel_url: `${base}/dashboard/plans?checkout=cancelled&selected=${input.tier}`,
    client_reference_id: input.tenantId,
    metadata,
    subscription_data: { metadata },
  };

  if (input.stripeCustomerId) {
    params.customer = input.stripeCustomerId;
  } else if (input.customerEmail) {
    params.customer_email = input.customerEmail;
  }

  return params;
}

// --- Webhook event handling -------------------------------------------------

/** The fields the webhook upserts onto a tenant's Subscription row. */
export interface SubscriptionUpsert {
  tenantId: string;
  tier: Tier;
  status: SubscriptionStatus;
  stripeSubscriptionId: string | null;
  stripeCustomerId: string | null;
  currentPeriodEnd: Date | null;
}

/**
 * Persistence boundary for the webhook — implemented by the Drizzle-backed
 * store at runtime and by a fake in tests. Must be idempotent.
 */
export interface SubscriptionStore {
  /**
   * Upsert the single Subscription row for a tenant. Returns `activated: true`
   * only on the transition INTO ACTIVE from a different (or absent) prior
   * status, so the provisioning hook fires exactly once.
   */
  upsertSubscription(data: SubscriptionUpsert): Promise<{ activated: boolean }>;
}

export interface WebhookHandlerDeps {
  store: SubscriptionStore;
  /** Invoked once when a subscription transitions to ACTIVE. Defaults to the stub. */
  onActivated?: (tenantId: string, subscriptionId: string) => void;
}

/** Read a string id that may arrive expanded (object with .id) or as a raw string. */
function idOf(
  ref: string | { id: string } | null | undefined,
): string | null {
  if (!ref) return null;
  return typeof ref === "string" ? ref : ref.id;
}

/** current_period_end moved onto subscription items in recent API versions; read either. */
function periodEndFrom(sub: Stripe.Subscription): Date | null {
  const item = sub.items?.data?.[0] as { current_period_end?: number } | undefined;
  const legacy = (sub as unknown as { current_period_end?: number })
    .current_period_end;
  const secs = item?.current_period_end ?? legacy;
  return typeof secs === "number" ? new Date(secs * 1000) : null;
}

function upsertFromSubscription(
  sub: Stripe.Subscription,
  env: NodeJS.ProcessEnv,
): SubscriptionUpsert | null {
  const tenantId = sub.metadata?.tenantId;
  if (!tenantId) return null; // not one of ours (or created outside the portal)
  const priceId = idOf(sub.items?.data?.[0]?.price as { id: string } | string);
  const tier =
    (sub.metadata?.tier as Tier | undefined) ?? tierForPriceId(priceId, env);
  if (!tier) return null;
  return {
    tenantId,
    tier,
    status: mapStripeSubscriptionStatus(sub.status),
    stripeSubscriptionId: sub.id,
    stripeCustomerId: idOf(sub.customer),
    currentPeriodEnd: periodEndFrom(sub),
  };
}

function upsertFromCheckoutSession(
  session: Stripe.Checkout.Session,
): SubscriptionUpsert | null {
  const tenantId = session.client_reference_id ?? session.metadata?.tenantId;
  const tier = session.metadata?.tier as Tier | undefined;
  if (!tenantId || !tier) return null;
  // Self-contained status: a completed+paid checkout is ACTIVE; the
  // customer.subscription.* events reconcile the authoritative lifecycle.
  const status: SubscriptionStatus =
    session.status === "complete" && session.payment_status === "paid"
      ? "ACTIVE"
      : "INCOMPLETE";
  return {
    tenantId,
    tier,
    status,
    stripeSubscriptionId: idOf(session.subscription),
    stripeCustomerId: idOf(session.customer),
    currentPeriodEnd: null,
  };
}

/**
 * Handle one verified Stripe event. Idempotent: safe to receive the same event
 * twice (the upsert is keyed on tenant, and the activation hook only fires on
 * the transition into ACTIVE).
 *
 * Returns whether the event was one we act on.
 */
export async function handleStripeWebhookEvent(
  event: Stripe.Event,
  deps: WebhookHandlerDeps,
  env: NodeJS.ProcessEnv = process.env,
): Promise<{ handled: boolean }> {
  let upsert: SubscriptionUpsert | null = null;

  switch (event.type) {
    case "checkout.session.completed":
      upsert = upsertFromCheckoutSession(
        event.data.object as Stripe.Checkout.Session,
      );
      break;
    case "customer.subscription.created":
    case "customer.subscription.updated":
    case "customer.subscription.deleted":
      upsert = upsertFromSubscription(
        event.data.object as Stripe.Subscription,
        env,
      );
      break;
    default:
      return { handled: false };
  }

  if (!upsert) return { handled: false };

  const { activated } = await deps.store.upsertSubscription(upsert);
  if (activated) {
    const onActivated = deps.onActivated ?? onSubscriptionActivated;
    onActivated(upsert.tenantId, upsert.stripeSubscriptionId ?? "");
  }
  return { handled: true };
}
