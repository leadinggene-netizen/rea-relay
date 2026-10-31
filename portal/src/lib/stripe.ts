/**
 * Stripe SDK client (server-only).
 *
 * The SDK is constructed LAZILY — never at import/build time — so a missing
 * STRIPE_SECRET_KEY does not break `next build` or the unit tests (which mock
 * this module). Call `getStripe()` only from server code that actually needs to
 * talk to Stripe (checkout creation, webhook handling).
 *
 * The API version is pinned to the version the installed SDK was generated
 * against, so upgrading the SDK is a deliberate, reviewed change rather than an
 * implicit behavioral shift.
 */
import Stripe from "stripe";

/**
 * Pinned Stripe API version. Keep in lockstep with the installed `stripe` SDK
 * (see node_modules/stripe apiVersion). Bump intentionally on SDK upgrade.
 */
export const STRIPE_API_VERSION = "2026-10-28.endive" as const;

let cached: Stripe | undefined;

/** True when a secret key is configured. Lets callers 503 cleanly instead of throwing a raw SDK error. */
export function isStripeConfigured(): boolean {
  return Boolean(process.env.STRIPE_SECRET_KEY);
}

/**
 * Lazily construct (and memoize) the Stripe client from STRIPE_SECRET_KEY.
 * Throws only when actually invoked without a key — never at import time.
 */
export function getStripe(): Stripe {
  if (cached) return cached;
  const key = process.env.STRIPE_SECRET_KEY;
  if (!key) {
    throw new Error("STRIPE_SECRET_KEY must be set to use the Stripe client");
  }
  cached = new Stripe(key, {
    apiVersion: STRIPE_API_VERSION,
    typescript: true,
    appInfo: { name: "rea-relay-portal" },
  });
  return cached;
}

/** Test-only: drop the memoized client so a fresh env is picked up. */
export function __resetStripeForTests(): void {
  cached = undefined;
}
