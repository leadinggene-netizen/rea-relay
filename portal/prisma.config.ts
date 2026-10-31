import { createRequire } from "node:module";
import { existsSync, writeFileSync } from "node:fs";
import path from "node:path";
import os from "node:os";
import { defineConfig } from "prisma/config";

/**
 * Prisma 8 config (replaces the deprecated package.json#prisma key).
 *
 * OFFLINE / AIR-GAPPED NOTE
 * -------------------------
 * Prisma 8 runs `validate`, `generate` and (with a driver adapter) `migrate`
 * through a WASM query compiler + WASM schema engine bundled in the `prisma`
 * npm package — it does NOT need the native Rust schema-engine binary from
 * binaries.prisma.sh. The CLI still *eagerly checks* that a schema-engine file
 * exists at startup, and if absent tries to download it. In an environment
 * where binaries.prisma.sh is unreachable that download fails.
 *
 * The documented fix is to point PRISMA_SCHEMA_ENGINE_BINARY at an existing
 * file so the CLI skips the download. Because validate/generate use the WASM
 * engine, the file is only a presence sentinel and is never executed. We point
 * it at the WASM schema engine that ships inside the `prisma` package; if that
 * cannot be resolved we fall back to a tiny placeholder in the temp dir. Unset
 * the env (or delete this block) in an environment with normal network access.
 */
function ensureSchemaEngineSentinel(): void {
  if (process.env.PRISMA_SCHEMA_ENGINE_BINARY) return;
  const require = createRequire(import.meta.url);
  let sentinel: string | undefined;
  try {
    const prismaPkg = require.resolve("prisma/package.json");
    const wasm = path.join(path.dirname(prismaPkg), "build", "schema_engine_bg.wasm");
    if (existsSync(wasm)) sentinel = wasm;
  } catch {
    // prisma not resolvable from here — fall through to placeholder.
  }
  if (!sentinel) {
    sentinel = path.join(os.tmpdir(), "prisma-schema-engine-sentinel");
    if (!existsSync(sentinel)) writeFileSync(sentinel, "");
  }
  process.env.PRISMA_SCHEMA_ENGINE_BINARY = sentinel;
  // Allow the CLI to proceed past the (now skipped) checksum verification.
  process.env.PRISMA_ENGINES_CHECKSUM_IGNORE_MISSING ??= "1";
}

ensureSchemaEngineSentinel();

export default defineConfig({
  schema: "prisma/schema.prisma",
  // Connection URL for Migrate (Prisma 8 moves it out of the schema file).
  // Not needed for validate/generate. Uncomment + set DATABASE_URL to migrate.
  // datasource: { url: process.env.DATABASE_URL },
  migrations: {
    seed: "tsx prisma/seed.ts",
  },
});
