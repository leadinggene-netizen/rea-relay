/**
 * Drizzle-backed implementation of the webhook's SubscriptionStore.
 *
 * Kept out of billing.ts so the pure logic there stays DB-free and offline
 * testable. This module is only imported by the webhook route at runtime; the
 * `db` client is itself lazy (see src/db/index.ts), so importing this does not
 * open a connection.
 *
 * The upsert is a read-modify-write keyed on tenantId (the schema models one
 * billing relationship per tenant). It is idempotent and reports the
 * transition INTO ACTIVE so the provisioning hook fires exactly once.
 */
import { eq } from "drizzle-orm";
import { db } from "@/db";
import { plans, subscriptions } from "@/db/schema";
import type { SubscriptionStore, SubscriptionUpsert } from "./billing";

type DbLike = typeof db;

export class DrizzleSubscriptionStore implements SubscriptionStore {
  constructor(private readonly client: DbLike = db) {}

  async upsertSubscription(
    data: SubscriptionUpsert,
  ): Promise<{ activated: boolean }> {
    // Resolve the Plan row id (FK) for the tier.
    const [plan] = await this.client
      .select({ id: plans.id })
      .from(plans)
      .where(eq(plans.tier, data.tier))
      .limit(1);
    if (!plan) {
      throw new Error(
        `No Plan row for tier ${data.tier} — run \`npm run db:seed\` first`,
      );
    }

    const [existing] = await this.client
      .select({ id: subscriptions.id, status: subscriptions.status })
      .from(subscriptions)
      .where(eq(subscriptions.tenantId, data.tenantId))
      .limit(1);

    const activated =
      data.status === "ACTIVE" && existing?.status !== "ACTIVE";

    if (existing) {
      await this.client
        .update(subscriptions)
        .set({
          planId: plan.id,
          status: data.status,
          stripeSubscriptionId: data.stripeSubscriptionId,
          stripeCustomerId: data.stripeCustomerId,
          currentPeriodEnd: data.currentPeriodEnd,
        })
        .where(eq(subscriptions.id, existing.id));
    } else {
      await this.client.insert(subscriptions).values({
        tenantId: data.tenantId,
        planId: plan.id,
        status: data.status,
        stripeSubscriptionId: data.stripeSubscriptionId,
        stripeCustomerId: data.stripeCustomerId,
        currentPeriodEnd: data.currentPeriodEnd,
      });
    }

    return { activated };
  }

  /** Look up the Stripe customer id already on file for a tenant, for reuse at checkout. */
  async stripeCustomerIdForTenant(tenantId: string): Promise<string | null> {
    const [row] = await this.client
      .select({ stripeCustomerId: subscriptions.stripeCustomerId })
      .from(subscriptions)
      .where(eq(subscriptions.tenantId, tenantId))
      .limit(1);
    return row?.stripeCustomerId ?? null;
  }
}
