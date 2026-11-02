/**
 * Activation-time provisioning: turn a tenant's newly-ACTIVE subscription into
 * a real, owner-scoped subscription on the Go relay.
 *
 * This is the real implementation of the hook that billing.ts stubs out
 * (`onSubscriptionActivated`). It is wired into the Stripe webhook as a
 * fire-and-log callback (see src/app/api/stripe/webhook/route.ts) so a slow or
 * failing relay never blocks or fails the webhook's 200 — all errors are caught
 * and logged here, never thrown out.
 *
 * It does NOT create any connections; that is Part 2. Server-only: the `db` and
 * relay clients are lazy, so importing this opens no socket.
 */
import { eq } from "drizzle-orm";
import { db } from "@/db";
import { plans, subscriptions } from "@/db/schema";
import { relayClientFromEnv } from "./relay";

type DbLike = typeof db;

/**
 * Link a tenant's Subscription to a relay subscription. Idempotent:
 * - no-op (log + return) if the relay is not configured;
 * - no-op if the tenant has no subscription/plan row;
 * - no-op if the subscription is already linked (`relaySubscriptionId` set);
 * - otherwise create the relay subscription (upsert) and persist the link.
 *
 * The portal Subscription row id is reused as the relay subscription id — it is
 * stable and unique, which keeps re-POSTs to the relay idempotent.
 *
 * `stripeSubscriptionId` is used only for log correlation. `client` is
 * injectable for tests (defaults to the shared lazy `db`).
 */
export async function provisionOnActivation(
  tenantId: string,
  stripeSubscriptionId: string,
  client: DbLike = db,
): Promise<void> {
  try {
    if (!process.env.RELAY_ADMIN_URL || !process.env.RELAY_ADMIN_TOKEN) {
      console.info("[provisioning] relay not configured; skipping");
      return;
    }

    const [row] = await client
      .select({
        id: subscriptions.id,
        relaySubscriptionId: subscriptions.relaySubscriptionId,
        tier: plans.tier,
        maxConnections: plans.maxConnections,
      })
      .from(subscriptions)
      .innerJoin(plans, eq(subscriptions.planId, plans.id))
      .where(eq(subscriptions.tenantId, tenantId))
      .limit(1);

    if (!row) {
      console.info(
        `[provisioning] no subscription/plan for tenant=${tenantId}; skipping`,
      );
      return;
    }

    if (row.relaySubscriptionId) {
      console.info(
        `[provisioning] already linked (tenant=${tenantId} relaySubscriptionId=${row.relaySubscriptionId}); skipping`,
      );
      return;
    }

    const relaySubId = row.id;
    await relayClientFromEnv().createSubscription({
      id: relaySubId,
      tier: row.tier,
      max_connections: row.maxConnections,
      owner_id: tenantId,
    });

    await client
      .update(subscriptions)
      .set({ relaySubscriptionId: relaySubId })
      .where(eq(subscriptions.id, relaySubId));

    console.info(
      `[provisioning] linked relay subscription ${relaySubId} for tenant=${tenantId} (stripe=${stripeSubscriptionId})`,
    );
  } catch (err) {
    // Never throw out: the webhook must still return 200 so Stripe does not
    // retry over a provisioning hiccup. The link is re-attempted on the next
    // activation event (and createSubscription is an upsert).
    console.error(
      `[provisioning] failed for tenant=${tenantId} (stripe=${stripeSubscriptionId}):`,
      err,
    );
  }
}
