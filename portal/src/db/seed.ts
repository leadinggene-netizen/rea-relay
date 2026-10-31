/**
 * Seed the mirrored plan catalog. Run manually against a live DB:
 *
 *   npm run db:seed      # tsx src/db/seed.ts
 *
 * NOT run during build. Prices come from src/lib/plans.ts (single source of
 * truth) and are placeholder competitive values marked TODO there.
 *
 * Requires DATABASE_URL to point at a migrated PostgreSQL (run
 * `npm run db:migrate` first).
 */
import { drizzle } from "drizzle-orm/postgres-js";
import postgres from "postgres";
import * as schema from "./schema";
import { PLANS } from "../lib/plans";

async function main() {
  const connectionString = process.env.DATABASE_URL;
  if (!connectionString) {
    throw new Error("DATABASE_URL must be set to seed the database");
  }
  const sql = postgres(connectionString);
  const db = drizzle(sql, { schema });

  try {
    for (const p of PLANS) {
      const [plan] = await db
        .insert(schema.plans)
        .values({
          tier: p.tier,
          name: p.name,
          maxConnections: p.maxConnections,
          monthlyPrice: p.monthlyPriceCents,
          // stripePriceId: wire in once Stripe products exist.
        })
        .onConflictDoUpdate({
          target: schema.plans.tier,
          set: {
            name: p.name,
            maxConnections: p.maxConnections,
            monthlyPrice: p.monthlyPriceCents,
          },
        })
        .returning();
      console.log(
        `seeded plan ${plan.tier} (${plan.maxConnections} conn, ${plan.monthlyPrice}c)`,
      );
    }
  } finally {
    await sql.end();
  }
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
