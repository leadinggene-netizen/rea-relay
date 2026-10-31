/**
 * Dashboard data source for a tenant's connections, behind an interface so the
 * portal builds and renders WITHOUT the Go relay running.
 *
 * - StubConnectionsProvider: in-memory demo rows (used when the relay is not
 *   configured, and in dev).
 * - RelayConnectionsProvider: reads live rows from the relay admin API.
 *
 * `getConnectionsProvider()` picks the real one only when RELAY_ADMIN_URL and
 * RELAY_ADMIN_TOKEN are both set; otherwise the stub. The build never contacts
 * the relay.
 */
import { relayClientFromEnv, type RelayConnection } from "./relay";

/** Normalized view model the dashboard renders. */
export interface ConnectionView {
  id: string;
  key: string;
  label: string;
  subscriptionId: string;
  brokerServer?: string;
  status: "pending" | "provisioning" | "active" | "paused" | "error";
}

export interface ConnectionsProvider {
  readonly source: "stub" | "relay";
  listForOwner(ownerId: string): Promise<ConnectionView[]>;
}

function fromRelay(c: RelayConnection): ConnectionView {
  const broker = c.accounts?.find((a) => a.broker)?.broker;
  return {
    id: c.id,
    key: c.key,
    label: c.id,
    subscriptionId: c.subscription_id,
    brokerServer: broker,
    // The relay DTO has no lifecycle status; treat a live, listed connection
    // as active. The portal's own Connection.status is authoritative once the
    // DB-backed slice lands.
    status: "active",
  };
}

export class RelayConnectionsProvider implements ConnectionsProvider {
  readonly source = "relay" as const;
  async listForOwner(ownerId: string): Promise<ConnectionView[]> {
    const client = relayClientFromEnv();
    const conns = await client.listConnections({ ownerId });
    return conns.map(fromRelay);
  }
}

export class StubConnectionsProvider implements ConnectionsProvider {
  readonly source = "stub" as const;
  async listForOwner(_ownerId: string): Promise<ConnectionView[]> {
    void _ownerId;
    return [
      {
        id: "demo-conn-1",
        key: "rk_demo_xauusd_london",
        label: "XAUUSD — London scalper",
        subscriptionId: "sub-demo",
        brokerServer: "ICMarkets-Demo",
        status: "active",
      },
      {
        id: "demo-conn-2",
        key: "rk_demo_pending",
        label: "EURUSD — swing (provisioning)",
        subscriptionId: "sub-demo",
        brokerServer: "ICMarkets-Demo",
        status: "provisioning",
      },
    ];
  }
}

export function getConnectionsProvider(): ConnectionsProvider {
  if (process.env.RELAY_ADMIN_URL && process.env.RELAY_ADMIN_TOKEN) {
    return new RelayConnectionsProvider();
  }
  return new StubConnectionsProvider();
}
