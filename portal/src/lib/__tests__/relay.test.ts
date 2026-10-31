import { describe, it, expect, vi, beforeEach } from "vitest";
import { createRelayClient, RelayAdminError } from "@/lib/relay";

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("relay admin client request building", () => {
  let fetchImpl: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    fetchImpl = vi.fn();
  });

  function client() {
    return createRelayClient({
      baseUrl: "http://relay.local:8080/",
      token: "secret-token",
      fetchImpl: fetchImpl as unknown as typeof fetch,
    });
  }

  it("createSubscription POSTs the DTO with a Bearer header", async () => {
    fetchImpl.mockResolvedValue(
      jsonResponse({ id: "sub1", tier: "pro", max_connections: 5 }),
    );
    const out = await client().createSubscription({
      id: "sub1",
      tier: "pro",
      max_connections: 5,
      owner_id: "tenant-42",
    });

    expect(fetchImpl).toHaveBeenCalledTimes(1);
    const [url, init] = fetchImpl.mock.calls[0];
    // trailing slash on baseUrl is normalized
    expect(url).toBe("http://relay.local:8080/admin/subscriptions");
    expect(init.method).toBe("POST");
    expect(init.headers.Authorization).toBe("Bearer secret-token");
    expect(init.headers["Content-Type"]).toBe("application/json");
    expect(JSON.parse(init.body)).toEqual({
      id: "sub1",
      tier: "pro",
      max_connections: 5,
      owner_id: "tenant-42",
    });
    expect(out.id).toBe("sub1");
  });

  it("createConnection POSTs to /admin/connections", async () => {
    fetchImpl.mockResolvedValue(
      jsonResponse({ id: "c1", key: "rk_abc", subscription_id: "sub1" }),
    );
    await client().createConnection({
      id: "c1",
      key: "rk_abc",
      subscription_id: "sub1",
    });
    const [url, init] = fetchImpl.mock.calls[0];
    expect(url).toBe("http://relay.local:8080/admin/connections");
    expect(init.method).toBe("POST");
  });

  it("listConnections builds owner_id + subscription_id query string", async () => {
    fetchImpl.mockResolvedValue(jsonResponse([]));
    await client().listConnections({
      ownerId: "tenant-42",
      subscriptionId: "sub1",
    });
    const [url, init] = fetchImpl.mock.calls[0];
    expect(url).toBe(
      "http://relay.local:8080/admin/connections?subscription_id=sub1&owner_id=tenant-42",
    );
    expect(init.method).toBe("GET");
    expect(init.headers.Authorization).toBe("Bearer secret-token");
  });

  it("listConnections omits the query string when no filter is given", async () => {
    fetchImpl.mockResolvedValue(jsonResponse([]));
    await client().listConnections();
    expect(fetchImpl.mock.calls[0][0]).toBe(
      "http://relay.local:8080/admin/connections",
    );
  });

  it("deleteConnection DELETEs the encoded id and tolerates 204", async () => {
    fetchImpl.mockResolvedValue(jsonResponse(null, 204));
    await client().deleteConnection("c 1/x");
    const [url, init] = fetchImpl.mock.calls[0];
    expect(url).toBe("http://relay.local:8080/admin/connections/c%201%2Fx");
    expect(init.method).toBe("DELETE");
  });

  it("throws a typed RelayAdminError on a non-2xx response", async () => {
    fetchImpl.mockResolvedValue(
      jsonResponse(
        { error: "connection_limit", message: "at max" },
        409,
      ),
    );
    await expect(
      client().createConnection({
        id: "c1",
        key: "rk_abc",
        subscription_id: "sub1",
      }),
    ).rejects.toMatchObject({
      name: "RelayAdminError",
      status: 409,
      code: "connection_limit",
    } satisfies Partial<RelayAdminError>);
  });
});
