/**
 * Broker-credential secret store.
 *
 * A "connection" needs the MT5 broker login/password the future hosted cell
 * will use. We never store that plaintext: `LocalSecretStore` (dev/default)
 * encrypts the JSON cred with AES-256-GCM using a key from `SECRET_STORE_KEY`
 * and persists only ciphertext/iv/authTag in the `BrokerSecret` table.
 *
 * In production (§4) `KeyVaultSecretStore` will keep creds in Azure Key Vault;
 * it is a stub here (throws) and the Azure SDK is deliberately NOT a dependency
 * yet. `getSecretStore()` picks Key Vault when `AZURE_KEY_VAULT_URL` is set.
 *
 * Server-only: uses Node's built-in `crypto` and the lazy `db` client, so
 * importing this module opens no socket and pulls in no native binary.
 */
import {
  createCipheriv,
  createDecipheriv,
  randomBytes,
} from "node:crypto";
import { eq } from "drizzle-orm";
import { db } from "@/db";
import { brokerSecrets } from "@/db/schema";

type DbLike = typeof db;

/** The MT5 broker credential a hosted connection authenticates with. */
export interface BrokerCred {
  server: string;
  login: string;
  password: string;
}

export interface SecretStore {
  putBrokerCred(connectionId: string, cred: BrokerCred): Promise<void>;
  getBrokerCred(connectionId: string): Promise<BrokerCred | null>;
  deleteBrokerCred(connectionId: string): Promise<void>;
}

const ALGORITHM = "aes-256-gcm";
const KEY_BYTES = 32; // AES-256
const IV_BYTES = 12; // 96-bit nonce, the GCM standard

/**
 * Decode and validate the 32-byte key from `SECRET_STORE_KEY` (base64).
 * Throws a clear error if it is missing or the wrong length.
 */
function loadKey(env: NodeJS.ProcessEnv): Buffer {
  const raw = env.SECRET_STORE_KEY;
  if (!raw) {
    throw new Error(
      "SECRET_STORE_KEY must be set (base64-encoded 32-byte key) to use LocalSecretStore",
    );
  }
  const key = Buffer.from(raw, "base64");
  if (key.length !== KEY_BYTES) {
    throw new Error(
      `SECRET_STORE_KEY must decode to ${KEY_BYTES} bytes (got ${key.length}) — generate one with \`openssl rand -base64 32\``,
    );
  }
  return key;
}

/**
 * Dev/default store: AES-256-GCM into the BrokerSecret table. The authTag makes
 * tampering detectable — `getBrokerCred` throws on a modified ciphertext/iv/tag
 * rather than returning garbage.
 */
export class LocalSecretStore implements SecretStore {
  constructor(
    private readonly client: DbLike = db,
    private readonly env: NodeJS.ProcessEnv = process.env,
  ) {}

  async putBrokerCred(connectionId: string, cred: BrokerCred): Promise<void> {
    const key = loadKey(this.env);
    const iv = randomBytes(IV_BYTES);
    const cipher = createCipheriv(ALGORITHM, key, iv);
    const plaintext = Buffer.from(JSON.stringify(cred), "utf8");
    const ciphertext = Buffer.concat([cipher.update(plaintext), cipher.final()]);
    const authTag = cipher.getAuthTag();

    const encoded = {
      ciphertext: ciphertext.toString("base64"),
      iv: iv.toString("base64"),
      authTag: authTag.toString("base64"),
    };

    await this.client
      .insert(brokerSecrets)
      .values({ connectionId, ...encoded })
      .onConflictDoUpdate({
        target: brokerSecrets.connectionId,
        set: { ...encoded, updatedAt: new Date() },
      });
  }

  async getBrokerCred(connectionId: string): Promise<BrokerCred | null> {
    const key = loadKey(this.env);
    const [row] = await this.client
      .select({
        ciphertext: brokerSecrets.ciphertext,
        iv: brokerSecrets.iv,
        authTag: brokerSecrets.authTag,
      })
      .from(brokerSecrets)
      .where(eq(brokerSecrets.connectionId, connectionId))
      .limit(1);
    if (!row) return null;

    const decipher = createDecipheriv(
      ALGORITHM,
      key,
      Buffer.from(row.iv, "base64"),
    );
    decipher.setAuthTag(Buffer.from(row.authTag, "base64"));
    // `final()` throws if the authTag does not verify (tampered data / wrong key).
    const plaintext = Buffer.concat([
      decipher.update(Buffer.from(row.ciphertext, "base64")),
      decipher.final(),
    ]);
    return JSON.parse(plaintext.toString("utf8")) as BrokerCred;
  }

  async deleteBrokerCred(connectionId: string): Promise<void> {
    await this.client
      .delete(brokerSecrets)
      .where(eq(brokerSecrets.connectionId, connectionId));
  }
}

/**
 * Production store (STUB until §4). Selected when `AZURE_KEY_VAULT_URL` is set.
 * The Azure SDK is intentionally NOT added yet, so every method throws.
 */
export class KeyVaultSecretStore implements SecretStore {
  constructor(readonly vaultUrl: string) {}

  private notImplemented(): never {
    throw new Error(
      "Key Vault secret store not yet implemented — set up in §4",
    );
  }

  async putBrokerCred(_connectionId: string, _cred: BrokerCred): Promise<void> {
    void _connectionId;
    void _cred;
    this.notImplemented();
  }

  async getBrokerCred(_connectionId: string): Promise<BrokerCred | null> {
    void _connectionId;
    this.notImplemented();
  }

  async deleteBrokerCred(_connectionId: string): Promise<void> {
    void _connectionId;
    this.notImplemented();
  }
}

/**
 * Pick the secret store for the current environment: Key Vault when
 * `AZURE_KEY_VAULT_URL` is set (production), otherwise the local AES store.
 */
export function getSecretStore(
  env: NodeJS.ProcessEnv = process.env,
): SecretStore {
  if (env.AZURE_KEY_VAULT_URL) {
    return new KeyVaultSecretStore(env.AZURE_KEY_VAULT_URL);
  }
  return new LocalSecretStore();
}
