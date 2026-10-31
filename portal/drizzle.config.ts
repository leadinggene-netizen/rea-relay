import { defineConfig } from "drizzle-kit";

/**
 * Drizzle Kit config for the rea-relay portal.
 *
 * OFFLINE NOTE
 * ------------
 * Drizzle Kit is pure TypeScript with zero native binaries. `drizzle-kit
 * generate` diffs `src/db/schema.ts` against the committed migration history
 * and emits SQL into `drizzle/` — it needs NO database and NO network, so it
 * runs fully offline here and in CI.
 *
 * `drizzle-kit migrate` applies those migrations and DOES need a live
 * PostgreSQL via DATABASE_URL (not run at build/test time).
 */
export default defineConfig({
  schema: "./src/db/schema.ts",
  out: "./drizzle",
  dialect: "postgresql",
  dbCredentials: {
    // Only read by `migrate` / `push`; `generate` ignores it. Empty string
    // keeps config evaluation offline-safe when the var is unset.
    url: process.env.DATABASE_URL ?? "",
  },
  casing: "snake_case",
  verbose: true,
  strict: true,
});
