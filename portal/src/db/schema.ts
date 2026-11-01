/**
 * Drizzle schema for the rea-relay portal (control plane).
 *
 * Multi-tenant model: Tenant -> User, with Subscription (per Plan) and
 * Connection rows that mirror the Go relay's admin-API resources. A Connection
 * is the portal-side record of a hosted MT5 "cell"; `relayConnectionId` links
 * it to the connection the relay admin API provisions.
 *
 * Ported 1:1 from the former Prisma schema: same models, relations, enums and
 * nullability. Ids are app-generated (crypto.randomUUID) via `$defaultFn`,
 * matching Prisma's client-side cuid() default (generated in JS, not the DB),
 * so no DB extension is required and migrations stay portable.
 *
 * NOTE: `drizzle-kit generate` needs no database — it diffs this file to emit
 * SQL. A live PostgreSQL (DATABASE_URL) is only required for `drizzle-kit
 * migrate` and the seed.
 */
import { randomUUID } from "node:crypto";
import { relations } from "drizzle-orm";
import {
  boolean,
  index,
  integer,
  pgEnum,
  pgTable,
  text,
  timestamp,
} from "drizzle-orm/pg-core";

// --- Enums -------------------------------------------------------------------

export const roleEnum = pgEnum("role", ["OWNER", "ADMIN", "MEMBER"]);
export const tierEnum = pgEnum("tier", ["STARTER", "ADVANCED", "PRO"]);
export const subscriptionStatusEnum = pgEnum("subscription_status", [
  "INCOMPLETE",
  "ACTIVE",
  "PAST_DUE",
  "CANCELED",
]);
export const connectionStatusEnum = pgEnum("connection_status", [
  "PENDING",
  "PROVISIONING",
  "ACTIVE",
  "PAUSED",
  "ERROR",
]);

// Shared column helpers (parity with Prisma @default(cuid()) / @default(now())
// / @updatedAt).
const id = () =>
  text("id")
    .primaryKey()
    .$defaultFn(() => randomUUID());
const createdAt = () => timestamp("created_at").defaultNow().notNull();
const updatedAt = () =>
  timestamp("updated_at")
    .defaultNow()
    .notNull()
    .$onUpdate(() => new Date());

// --- Tables ------------------------------------------------------------------

/** A customer organization. The unit of isolation for billing + provisioning. */
export const tenants = pgTable("Tenant", {
  id: id(),
  name: text("name").notNull(),
  createdAt: createdAt(),
  updatedAt: updatedAt(),
});

/** A person who signs in. Belongs to exactly one Tenant. */
export const users = pgTable(
  "User",
  {
    id: id(),
    email: text("email").notNull().unique(),
    name: text("name"),
    role: roleEnum("role").default("MEMBER").notNull(),
    tenantId: text("tenant_id")
      .notNull()
      .references(() => tenants.id, { onDelete: "cascade" }),
    createdAt: createdAt(),
    updatedAt: updatedAt(),
  },
  (t) => [index("User_tenantId_idx").on(t.tenantId)],
);

/**
 * A purchasable tier. Mirrors the incumbent's connection-count tiers.
 * Prices are stored in cents to avoid float drift.
 */
export const plans = pgTable("Plan", {
  id: id(),
  tier: tierEnum("tier").notNull().unique(),
  name: text("name").notNull(),
  maxConnections: integer("max_connections").notNull(),
  // minor units (cents, USD)
  monthlyPrice: integer("monthly_price").notNull(),
  stripePriceId: text("stripe_price_id"),
  active: boolean("active").default(true).notNull(),
  createdAt: createdAt(),
  updatedAt: updatedAt(),
});

/** A tenant's active (or lapsed) plan. One per billing relationship. */
export const subscriptions = pgTable(
  "Subscription",
  {
    id: id(),
    tenantId: text("tenant_id")
      .notNull()
      .references(() => tenants.id, { onDelete: "cascade" }),
    planId: text("plan_id")
      .notNull()
      .references(() => plans.id),
    status: subscriptionStatusEnum("status").default("INCOMPLETE").notNull(),
    currentPeriodEnd: timestamp("current_period_end"),
    stripeSubscriptionId: text("stripe_subscription_id"),
    /** The Stripe Customer this tenant's billing is attached to (reused across checkouts). */
    stripeCustomerId: text("stripe_customer_id"),
    /** Mirrors the subscription id used in the relay admin API (owner_id scoping). */
    relaySubscriptionId: text("relay_subscription_id"),
    createdAt: createdAt(),
    updatedAt: updatedAt(),
  },
  (t) => [
    index("Subscription_tenantId_idx").on(t.tenantId),
    index("Subscription_planId_idx").on(t.planId),
  ],
);

/** A single hosted bridge cell. Counts against the plan's maxConnections. */
export const connections = pgTable(
  "Connection",
  {
    id: id(),
    tenantId: text("tenant_id")
      .notNull()
      .references(() => tenants.id, { onDelete: "cascade" }),
    subscriptionId: text("subscription_id")
      .notNull()
      .references(() => subscriptions.id, { onDelete: "cascade" }),
    /** The webhook key clients post to on the relay. */
    key: text("key").notNull().unique(),
    label: text("label").notNull(),
    brokerServer: text("broker_server"),
    status: connectionStatusEnum("status").default("PENDING").notNull(),
    /** The connection id returned by the relay admin API once provisioned. */
    relayConnectionId: text("relay_connection_id"),
    createdAt: createdAt(),
    updatedAt: updatedAt(),
  },
  (t) => [
    index("Connection_tenantId_idx").on(t.tenantId),
    index("Connection_subscriptionId_idx").on(t.subscriptionId),
  ],
);

// --- Relations ---------------------------------------------------------------

export const tenantsRelations = relations(tenants, ({ many }) => ({
  users: many(users),
  subscriptions: many(subscriptions),
  connections: many(connections),
}));

export const usersRelations = relations(users, ({ one }) => ({
  tenant: one(tenants, {
    fields: [users.tenantId],
    references: [tenants.id],
  }),
}));

export const plansRelations = relations(plans, ({ many }) => ({
  subscriptions: many(subscriptions),
}));

export const subscriptionsRelations = relations(
  subscriptions,
  ({ one, many }) => ({
    tenant: one(tenants, {
      fields: [subscriptions.tenantId],
      references: [tenants.id],
    }),
    plan: one(plans, {
      fields: [subscriptions.planId],
      references: [plans.id],
    }),
    connections: many(connections),
  }),
);

export const connectionsRelations = relations(connections, ({ one }) => ({
  tenant: one(tenants, {
    fields: [connections.tenantId],
    references: [tenants.id],
  }),
  subscription: one(subscriptions, {
    fields: [connections.subscriptionId],
    references: [subscriptions.id],
  }),
}));

// --- Inferred types (replace the generated Prisma model types) ---------------

export type Tenant = typeof tenants.$inferSelect;
export type NewTenant = typeof tenants.$inferInsert;
export type User = typeof users.$inferSelect;
export type NewUser = typeof users.$inferInsert;
export type Plan = typeof plans.$inferSelect;
export type NewPlan = typeof plans.$inferInsert;
export type Subscription = typeof subscriptions.$inferSelect;
export type NewSubscription = typeof subscriptions.$inferInsert;
export type Connection = typeof connections.$inferSelect;
export type NewConnection = typeof connections.$inferInsert;

export type Role = (typeof roleEnum.enumValues)[number];
export type Tier = (typeof tierEnum.enumValues)[number];
export type SubscriptionStatus =
  (typeof subscriptionStatusEnum.enumValues)[number];
export type ConnectionStatus =
  (typeof connectionStatusEnum.enumValues)[number];
