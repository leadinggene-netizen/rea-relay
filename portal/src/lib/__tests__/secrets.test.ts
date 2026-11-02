/**
 * Unit tests for the secret store — fully offline. The DB is a tiny in-memory
 * fake (one BrokerSecret row keyed by connectionId) and the AES key is a fixed
 * base64 32-byte test key, so the crypto is real but nothing touches Postgres.
 */
import { describe, it, expect } from "vitest";
import {
  LocalSecretStore,
  KeyVaultSecretStore,
  getSecretStore,
  type BrokerCred,
} from "@/lib/secrets";

// 32 bytes of 0x01..0x20, base64-encoded — a valid AES-256 key for tests.
const TEST_KEY = Buffer.from(
  Array.from({ length: 32 }, (_, i) => i + 1),
).toString("base64");

const TEST_ENV = { SECRET_STORE_KEY: TEST_KEY } as unknown as NodeJS.ProcessEnv;

interface SecretRow {
  connectionId: string;
  ciphertext: string;
  iv: string;
  authTag: string;
}

/**
 * In-memory fake supporting the exact query surface LocalSecretStore uses:
 * insert().values().onConflictDoUpdate(), select().from().where().limit(),
 * delete().where(). The real `eq(...)` filter is an opaque SQL object we can't
 * read here, so the fake keys rows by connectionId on insert and treats the
 * store as holding a single connection per test (each test uses one id) — the
 * where() argument is therefore ignored.
 */
function fakeDb(store: Map<string, SecretRow>) {
  return {
    insert() {
      return {
        values(values: SecretRow) {
          return {
            onConflictDoUpdate(args: { set: Partial<SecretRow> }) {
              const existing = store.get(values.connectionId);
              store.set(
                values.connectionId,
                existing
                  ? { ...existing, ...args.set, connectionId: values.connectionId }
                  : values,
              );
              return Promise.resolve();
            },
          };
        },
      };
    },
    select() {
      const builder = {
        from() {
          return builder;
        },
        where() {
          return builder;
        },
        limit() {
          const row = [...store.values()][0];
          return Promise.resolve(row ? [row] : []);
        },
      };
      return builder;
    },
    delete() {
      return {
        where() {
          store.clear();
          return Promise.resolve();
        },
      };
    },
  };
}

const CRED: BrokerCred = {
  server: "ICMarkets-Demo",
  login: "123456",
  password: "s3cr3t-pa$$",
};

describe("LocalSecretStore", () => {
  it("round-trips a broker cred (put then get returns the same value)", async () => {
    const store = new Map<string, SecretRow>();
    const db = fakeDb(store);
    const sec = new LocalSecretStore(db as never, TEST_ENV);

    await sec.putBrokerCred("conn-1", CRED);
    const got = await sec.getBrokerCred("conn-1");
    expect(got).toEqual(CRED);
  });

  it("stores only ciphertext — the plaintext password is never persisted", async () => {
    const store = new Map<string, SecretRow>();
    const sec = new LocalSecretStore(fakeDb(store) as never, TEST_ENV);
    await sec.putBrokerCred("conn-1", CRED);

    const row = store.get("conn-1")!;
    const blob = `${row.ciphertext}${row.iv}${row.authTag}`;
    expect(blob).not.toContain("s3cr3t-pa$$");
    expect(blob).not.toContain("ICMarkets-Demo");
  });

  it("returns null when there is no secret for the connection", async () => {
    const sec = new LocalSecretStore(fakeDb(new Map()) as never, TEST_ENV);
    expect(await sec.getBrokerCred("missing")).toBeNull();
  });

  it("fails authentication when the ciphertext is tampered with", async () => {
    const store = new Map<string, SecretRow>();
    const sec = new LocalSecretStore(fakeDb(store) as never, TEST_ENV);
    await sec.putBrokerCred("conn-1", CRED);

    // Flip a byte in the ciphertext.
    const row = store.get("conn-1")!;
    const bytes = Buffer.from(row.ciphertext, "base64");
    bytes[0] ^= 0xff;
    store.set("conn-1", { ...row, ciphertext: bytes.toString("base64") });

    await expect(sec.getBrokerCred("conn-1")).rejects.toThrow();
  });

  it("deletes a stored secret", async () => {
    const store = new Map<string, SecretRow>();
    const sec = new LocalSecretStore(fakeDb(store) as never, TEST_ENV);
    await sec.putBrokerCred("conn-1", CRED);
    await sec.deleteBrokerCred("conn-1");
    expect(store.has("conn-1")).toBe(false);
  });

  it("throws a clear error when SECRET_STORE_KEY is missing", async () => {
    const sec = new LocalSecretStore(
      fakeDb(new Map()) as never,
      {} as NodeJS.ProcessEnv,
    );
    await expect(sec.putBrokerCred("conn-1", CRED)).rejects.toThrow(
      /SECRET_STORE_KEY/,
    );
  });

  it("throws when the key does not decode to 32 bytes", async () => {
    const sec = new LocalSecretStore(fakeDb(new Map()) as never, {
      SECRET_STORE_KEY: Buffer.from("too-short").toString("base64"),
    } as unknown as NodeJS.ProcessEnv);
    await expect(sec.putBrokerCred("conn-1", CRED)).rejects.toThrow(/32 bytes/);
  });
});

describe("KeyVaultSecretStore (stub)", () => {
  it("throws not-implemented for every method", async () => {
    const kv = new KeyVaultSecretStore("https://vault.example/");
    await expect(kv.putBrokerCred("c", CRED)).rejects.toThrow(/not yet implemented/);
    await expect(kv.getBrokerCred("c")).rejects.toThrow(/not yet implemented/);
    await expect(kv.deleteBrokerCred("c")).rejects.toThrow(/not yet implemented/);
  });
});

describe("getSecretStore", () => {
  it("returns LocalSecretStore when AZURE_KEY_VAULT_URL is unset", () => {
    expect(getSecretStore({} as NodeJS.ProcessEnv)).toBeInstanceOf(
      LocalSecretStore,
    );
  });

  it("returns KeyVaultSecretStore when AZURE_KEY_VAULT_URL is set", () => {
    const s = getSecretStore({
      AZURE_KEY_VAULT_URL: "https://vault.example/",
    } as unknown as NodeJS.ProcessEnv);
    expect(s).toBeInstanceOf(KeyVaultSecretStore);
  });
});
