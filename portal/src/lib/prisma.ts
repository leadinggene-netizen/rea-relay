/**
 * Prisma client singleton.
 *
 * Prisma 8's `prisma-client` generator produces a driver-adapter client (no
 * bundled query-engine binary), so the constructor takes a driver adapter —
 * here @prisma/adapter-pg over DATABASE_URL.
 *
 * This module is NOT imported during the static build of any page in the
 * scaffold (pages use the stubbed providers / static plan catalog), so the
 * build needs no database. It is lazily initialized: the adapter + client are
 * created on first access, and only then is DATABASE_URL required. Unit tests
 * mock this module rather than connecting.
 */
import { PrismaClient } from "@/generated/prisma/client";
import { PrismaPg } from "@prisma/adapter-pg";

const globalForPrisma = globalThis as unknown as {
  prisma: PrismaClient | undefined;
};

function createClient(): PrismaClient {
  const connectionString = process.env.DATABASE_URL;
  if (!connectionString) {
    throw new Error("DATABASE_URL must be set to use the Prisma client");
  }
  const adapter = new PrismaPg({ connectionString });
  return new PrismaClient({
    adapter,
    log: process.env.NODE_ENV === "development" ? ["query", "error", "warn"] : ["error"],
  });
}

/**
 * Lazily-created shared client. Access `prisma` to get it; construction (and
 * the DATABASE_URL requirement) is deferred until first use.
 */
export const prisma: PrismaClient = new Proxy({} as PrismaClient, {
  get(_target, prop, receiver) {
    const client = (globalForPrisma.prisma ??= createClient());
    return Reflect.get(client, prop, receiver);
  },
});
