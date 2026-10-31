/**
 * Typed client for the Go relay's admin API (internal/adminapi).
 *
 * All calls are Bearer-authenticated with RELAY_ADMIN_TOKEN and target
 * RELAY_ADMIN_URL. The relay DISABLES the admin surface when it has no token
 * (every route 404s), so this client is only useful once the relay is
 * configured with a matching REA_ADMIN_TOKEN.
 *
 * This module is NOT called at build time. The provisioning flow (plan ->
 * checkout -> cell) will call it in a later slice. Request-building is unit
 * tested with a mocked fetch.
 */

/** Wire shape of a subscription (adminapi.subscriptionDTO). */
export interface RelaySubscription {
  id: string;
  tier: string;
  max_connections: number;
  owner_id?: string;
}

/** Wire shape of a broker symbol spec (adminapi.brokerSymbolSpecDTO). */
export interface RelayBrokerSymbolSpec {
  broker_symbol: string;
  pip_size: number;
  pip_value_per_lot: number;
  lot_min: number;
  lot_max: number;
  lot_step: number;
}

/** Wire shape of an account (adminapi.accountDTO). */
export interface RelayAccount {
  id: string;
  broker?: string;
  login_label?: string;
  mode?: string;
  equity?: number;
}

/** Wire shape of a connection (adminapi.connectionDTO). */
export interface RelayConnection {
  id: string;
  key: string;
  subscription_id: string;
  secret?: string;
  require_signature?: boolean;
  expiry?: string; // RFC3339
  symbol_table?: Record<string, RelayBrokerSymbolSpec>;
  accounts?: RelayAccount[];
}

export interface RelayClientConfig {
  /** Base URL, e.g. http://localhost:8080. No trailing slash required. */
  baseUrl: string;
  token: string;
  /** Injectable for testing; defaults to global fetch. */
  fetchImpl?: typeof fetch;
}

export class RelayAdminError extends Error {
  constructor(
    public readonly status: number,
    public readonly code: string,
    message: string,
  ) {
    super(message);
    this.name = "RelayAdminError";
  }
}

/** Build a client from explicit config. */
export function createRelayClient(config: RelayClientConfig) {
  const base = config.baseUrl.replace(/\/+$/, "");
  const doFetch = config.fetchImpl ?? fetch;

  function headers(): Record<string, string> {
    return {
      Authorization: `Bearer ${config.token}`,
      "Content-Type": "application/json",
      Accept: "application/json",
    };
  }

  async function parse<T>(res: Response): Promise<T> {
    if (res.status === 204) {
      return undefined as T;
    }
    const text = await res.text();
    const body = text ? JSON.parse(text) : undefined;
    if (!res.ok) {
      const code = (body && body.error) || "relay_error";
      const message = (body && body.message) || res.statusText;
      throw new RelayAdminError(res.status, code, message);
    }
    return body as T;
  }

  return {
    /** POST /admin/subscriptions — create or update a subscription. */
    async createSubscription(
      input: RelaySubscription,
    ): Promise<RelaySubscription> {
      const res = await doFetch(`${base}/admin/subscriptions`, {
        method: "POST",
        headers: headers(),
        body: JSON.stringify(input),
      });
      return parse<RelaySubscription>(res);
    },

    /** POST /admin/connections — create or update a connection. */
    async createConnection(input: RelayConnection): Promise<RelayConnection> {
      const res = await doFetch(`${base}/admin/connections`, {
        method: "POST",
        headers: headers(),
        body: JSON.stringify(input),
      });
      return parse<RelayConnection>(res);
    },

    /**
     * GET /admin/connections — list connections, optionally scoped to a
     * subscription or tenant (owner_id).
     */
    async listConnections(filter?: {
      subscriptionId?: string;
      ownerId?: string;
    }): Promise<RelayConnection[]> {
      const params = new URLSearchParams();
      if (filter?.subscriptionId)
        params.set("subscription_id", filter.subscriptionId);
      if (filter?.ownerId) params.set("owner_id", filter.ownerId);
      const qs = params.toString();
      const url = `${base}/admin/connections${qs ? `?${qs}` : ""}`;
      const res = await doFetch(url, { method: "GET", headers: headers() });
      return parse<RelayConnection[]>(res);
    },

    /** DELETE /admin/connections/{id} — remove a connection. */
    async deleteConnection(id: string): Promise<void> {
      const res = await doFetch(
        `${base}/admin/connections/${encodeURIComponent(id)}`,
        { method: "DELETE", headers: headers() },
      );
      await parse<void>(res);
    },
  };
}

export type RelayClient = ReturnType<typeof createRelayClient>;

/**
 * Build a client from environment (RELAY_ADMIN_URL / RELAY_ADMIN_TOKEN).
 * Throws if either is missing — call only from server code that needs it.
 */
export function relayClientFromEnv(): RelayClient {
  const baseUrl = process.env.RELAY_ADMIN_URL;
  const token = process.env.RELAY_ADMIN_TOKEN;
  if (!baseUrl || !token) {
    throw new Error(
      "RELAY_ADMIN_URL and RELAY_ADMIN_TOKEN must be set to call the relay admin API",
    );
  }
  return createRelayClient({ baseUrl, token });
}
