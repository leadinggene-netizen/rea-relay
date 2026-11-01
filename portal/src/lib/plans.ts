/**
 * Plan catalog + pricing helpers (pure logic — unit tested, no I/O).
 *
 * These definitions are the single source of truth for the marketing pricing
 * table AND the Drizzle seed. Prices are deliberately set BELOW the incumbent
 * (PineConnector-style tiers at ~$39 / $79 / $159 per month) to undercut it.
 *
 * TODO(pricing): these are placeholder competitive prices — revisit before
 * launch once hosting cost per MT5 cell and target margin are known.
 */

export type Tier = "STARTER" | "ADVANCED" | "PRO";

export interface PlanDef {
  tier: Tier;
  name: string;
  /** Concurrent hosted connections (MT5 cells) included. */
  maxConnections: number;
  /** Monthly price in minor units (USD cents). */
  monthlyPriceCents: number;
  /** Incumbent's comparable monthly price in cents, for the "vs" badge. */
  incumbentPriceCents: number;
  tagline: string;
  features: string[];
  highlighted?: boolean;
}

/**
 * TODO(pricing): placeholder, adjustable. Each tier undercuts the incumbent
 * reference price on the same line.
 */
export const PLANS: readonly PlanDef[] = [
  {
    tier: "STARTER",
    name: "Starter",
    maxConnections: 1,
    monthlyPriceCents: 2900, // TODO: $29 — incumbent ~$39
    incumbentPriceCents: 3900,
    tagline: "One strategy, one broker. Get your first alert bridged in minutes.",
    features: [
      "1 concurrent connection",
      "TradingView → MT4/MT5 relay",
      "Server-side risk & volume resolution",
      "Email support",
    ],
  },
  {
    tier: "ADVANCED",
    name: "Advanced",
    maxConnections: 3,
    monthlyPriceCents: 6900, // TODO: $69 — incumbent ~$79
    incumbentPriceCents: 7900,
    tagline: "Run a few strategies or accounts in parallel.",
    features: [
      "3 concurrent connections",
      "Everything in Starter",
      "Multi-account fan-out",
      "Priority email support",
    ],
    highlighted: true,
  },
  {
    tier: "PRO",
    name: "Pro",
    maxConnections: 5,
    monthlyPriceCents: 14900, // TODO: $149 — incumbent ~$159
    incumbentPriceCents: 15900,
    tagline: "Scale a book of strategies across brokers.",
    features: [
      "5 concurrent connections",
      "Everything in Advanced",
      "Offline buffer & resume replay",
      "Priority support",
    ],
  },
] as const;

/** Look up a plan by tier. Returns undefined for an unknown tier. */
export function getPlan(tier: string): PlanDef | undefined {
  return PLANS.find((p) => p.tier === tier);
}

/** Format a cents amount as a USD price string, e.g. 2900 -> "$29". */
export function formatPrice(cents: number): string {
  const dollars = cents / 100;
  const hasCents = cents % 100 !== 0;
  return `$${dollars.toLocaleString("en-US", {
    minimumFractionDigits: hasCents ? 2 : 0,
    maximumFractionDigits: 2,
  })}`;
}

/** Effective monthly cost of one connection on a plan, in cents (rounded). */
export function pricePerConnectionCents(plan: PlanDef): number {
  if (plan.maxConnections <= 0) return plan.monthlyPriceCents;
  return Math.round(plan.monthlyPriceCents / plan.maxConnections);
}

/**
 * Annual price in cents for a plan, applying a whole-months-free discount.
 * `monthsFree` months are not charged across the 12-month term.
 */
export function annualPriceCents(plan: PlanDef, monthsFree = 2): number {
  const billedMonths = Math.max(0, 12 - monthsFree);
  return plan.monthlyPriceCents * billedMonths;
}

/** Monthly savings vs the incumbent, in cents. Never negative. */
export function savingsVsIncumbentCents(plan: PlanDef): number {
  return Math.max(0, plan.incumbentPriceCents - plan.monthlyPriceCents);
}

/** Percentage (0–100, rounded) cheaper than the incumbent for the tier. */
export function savingsVsIncumbentPct(plan: PlanDef): number {
  if (plan.incumbentPriceCents <= 0) return 0;
  return Math.round(
    (savingsVsIncumbentCents(plan) / plan.incumbentPriceCents) * 100,
  );
}
