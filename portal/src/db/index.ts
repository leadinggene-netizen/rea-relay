/**
 * Drizzle database client (postgres.js driver).
 *
 * postgres.js is pure JavaScript — no native addon, no downloaded binary — so
 * this builds, migrates and runs fully offline.
 *
 * This module is NOT imported during the static build of any page in the
 * scaffold (pages use the stubbed providers / static plan catalog), so the
 * build needs no database. It is lazily initialized: the connection + Drizzle
 * client are created on first property access, and only then is DATABASE_URL
 * required — the build graph never opens a socket. Unit tests mock this module
 * rather than connecting.
 */
import { drizzle, type PostgresJsDatabase } from "drizzle-orm/postgres-js";
import postgres from "postgres";
import * as schema from "./schema";

export { schema };

type DB = PostgresJsDatabase<typeof schema>;

const globalForDb = globalThis as unknown as {
  db: DB | undefined;
  sql: ReturnType<typeof postgres> | undefined;
};

function createClient(): DB {
  const connectionString = process.env.DATABASE_URL;
  if (!connectionString) {
    throw new Error("DATABASE_URL must be set to use the database client");
  }
  const sql = (globalForDb.sql ??= postgres(connectionString));
  return drizzle(sql, {
    schema,
    logger: process.env.NODE_ENV === "development",
  });
}

/**
 * Lazily-created shared Drizzle client. Access `db` to get it; construction
 * (and the DATABASE_URL requirement) is deferred until first use, preserving
 * the lazy-proxy behavior the Prisma version had.
 */
export const db: DB = new Proxy({} as DB, {
  get(_target, prop, receiver) {
    const client = (globalForDb.db ??= createClient());
    return Reflect.get(client, prop, receiver);
  },
});
