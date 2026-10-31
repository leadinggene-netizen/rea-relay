/**
 * Seed the mirrored plan catalog. Run manually against a live DB:
 *
 *   npm run db:seed      # tsx prisma/seed.ts
 *
 * NOT run during build. Prices come from src/lib/plans.ts (single source of
 * truth) and are placeholder competitive values marked TODO there.
 */
import { PrismaClient, type $Enums } from "../src/generated/prisma/client";
import { PrismaPg } from "@prisma/adapter-pg";
import { PLANS } from "../src/lib/plans";

const adapter = new PrismaPg({
  connectionString: process.env.DATABASE_URL ?? "",
});
const prisma = new PrismaClient({ adapter });

async function main() {
  for (const p of PLANS) {
    const plan = await prisma.plan.upsert({
      where: { tier: p.tier as $Enums.Tier },
      update: {
        name: p.name,
        maxConnections: p.maxConnections,
        monthlyPrice: p.monthlyPriceCents,
      },
      create: {
        tier: p.tier as $Enums.Tier,
        name: p.name,
        maxConnections: p.maxConnections,
        monthlyPrice: p.monthlyPriceCents,
        // stripePriceId: wire in once Stripe products exist.
      },
    });
    console.log(`seeded plan ${plan.tier} (${plan.maxConnections} conn, ${plan.monthlyPrice}c)`);
  }
}

main()
  .then(async () => {
    await prisma.$disconnect();
  })
  .catch(async (e) => {
    console.error(e);
    await prisma.$disconnect();
    process.exit(1);
  });
