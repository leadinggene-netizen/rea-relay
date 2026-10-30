# TRANSPORT-REPORT — v2 real transport slice (spec §13)

Scope: the real transport (TR-1..TR-8) added on top of the v1 relay core. The Go
relay side is the testable deliverable; the MQL5 terminal agent under `ea/` is a
reference deliverable and is **not compiled or tested in this environment**.

Hard constraint honored: **Go standard library only** — the module has no
third-party dependencies (`go.mod` declares none). The WebSocket server is
hand-rolled per RFC 6455 (net/http Hijack upgrade, `crypto/sha1` +
`encoding/base64` accept key, from-scratch frame reader/writer with masking,
control frames and fragmentation).

## Go test summary

```
go vet ./...      # clean
go test ./...     # all packages pass
```

All pre-existing tests stay green (32) and the transport slice adds 10, for 42
total. Full-suite `-race` run is clean.

| Package | Result |
|---|---|
| `internal/auth` | ok |
| `internal/relay` | ok (TC-N1..N6, TC-E1..E7, TC-X1..X6, EC-10, EC-15 — 32 cases) |
| `internal/risk` | ok |
| `internal/transport` | ok — 10 new tests (below) |

New transport tests (`internal/transport`):

| Test | Covers |
|---|---|
| `TestAcceptKeyVector` | RFC 6455 Sec-WebSocket-Accept, known vector |
| `TestWSHandshakeAccepted` | TR-2 handshake accepted with valid auth → `welcome` + session_id |
| `TestWSHandshakeSignature` | TR-2 / TC-X2 bad HMAC → `bad_signature` (401, audited); correct HMAC accepted |
| `TestWSOrderDeliveredThenAcked` | TR-3/TR-4 order with monotonic `delivery_seq` delivered, acked + resulted (fill); resume token advances |
| `TestWSUnackedRedeliveredOnReconnect` | TR-4 unacked order redelivered **once** on reconnect within window; TR-6 dropped `delivery_expired` beyond window |
| `TestWSHeartbeatOffline` | TR-5 missed-heartbeat threshold flips `Online()` false and audits the offline transition |
| `TestWSHeartbeatPong` | TR-5 heartbeat frame answered with pong |
| `TestLongPollDeliverAndResult` | TR-7 long-poll GET delivers order, at-least-once re-return before ack, POST result clears it |
| `TestLongPollAuthRejected` | TR-7 auth parity — unknown key → 401 |
| `TestLongPollExpiry` | TR-7/TR-6 long-poll unacked order aged past window → `delivery_expired`, not re-returned |

## TR-1..TR-8 coverage

| Req | What it requires | Status | Where / evidence |
|---|---|---|---|
| **TR-1** | Primary transport WSS, agent-dials-out | Implemented + tested | `ws.go` stdlib RFC 6455 server; `TestWSHandshakeAccepted`. TLS terminates upstream of the handler (HTTP in-process for tests), same as the webhook path. |
| **TR-2** | Handshake: `connection_key` + optional HMAC(ts+nonce); returns `session_id` + `resume_token` | Implemented + tested | `server.go authenticate` (reuses `internal/auth.Signer`/`Compute`); `endpoint.go attach`; `TestWSHandshakeAccepted`, `TestWSHandshakeSignature`. |
| **TR-3** | Typed JSON envelope: order/ack/result/heartbeat/pong/control/error | Implemented + tested | `envelope.go`; order+ack+result exercised by `TestWSOrderDeliveredThenAcked`; heartbeat/pong by `TestWSHeartbeatPong`. `control` (pause/resume) and `error` types are defined and emitted (error on handshake reject / connection_limit); `control` push is defined but pause/resume remains enforced server-side via the delivery.Channel (see assumptions). |
| **TR-4** | At-least-once, monotonic per-connection `delivery_seq`, acks, redeliver unacked on reconnect within 60 s, dedup by idempotency key, ordering preserved | Implemented + tested | `endpoint.go` (nextSeq, pending map, attach-replay); `TestWSOrderDeliveredThenAcked`, `TestWSUnackedRedeliveredOnReconnect`. Agent-side idempotency dedup is implemented in the MQL5 agent (reference) and modeled by `MockAgent` in the delivery tests. |
| **TR-5** | Heartbeat 15 s; missed threshold → offline + surfaced | Implemented + tested | `endpoint.go liveLocked` (threshold × interval); `TestWSHeartbeatOffline`, `TestWSHeartbeatPong`. |
| **TR-6** | Reconnect exponential backoff + resume; beyond window → `delivery_expired` | Partially tested | Relay side: window expiry → `delivery_expired` tested (`TestWSUnackedRedeliveredOnReconnect` beyond-window branch, `TestLongPollExpiry`). Backoff is a **client** concern implemented in the MQL5 agent (`OnTimer`), reference-only. |
| **TR-7** | HTTP long-poll fallback, identical envelope + auth + at-least-once + idempotency | Implemented + tested | `longpoll.go`; `TestLongPollDeliverAndResult`, `TestLongPollAuthRejected`, `TestLongPollExpiry`. |
| **TR-8** | MQL5 agent on native TLS sockets (no DLL), RFC 6455 in MQL5, auth, execute FR-12..16, ack/result, heartbeat, reconnect-resume | Reference only — **not tested here** | `ea/TerminalAgent.mq5` + `ea/include/*.mqh`; relay side is transport-agnostic behind `delivery.Transport`. See `ea/README.md` for the compile/verify checklist. |

Reused (not re-implemented) delivery semantics behind `delivery.Transport`
(FR-8..11): fan-out to multiple accounts, pause/resume, per-connection ordering,
and the offline buffer + resume-token replay all remain in `internal/delivery`
and are exercised by the existing suite. The transport endpoints plug in as the
`Transport` for a connection via `relay.RegisterAgent`, which also enforces the
tier concurrency limit (FR-17/EC-14) at connect time.

## Spec assumptions made for this slice

1. **Handshake transport of auth.** TR-2 says the agent "presents its
   connection_key and … HMAC signature over the handshake with timestamp +
   nonce." The spec does not pin *where* on the wire. Decision: carry them on the
   WebSocket **HTTP upgrade request** (and the long-poll request) as headers
   (`X-Connection-Key`, `X-Timestamp`, `X-Nonce`, `X-Signature`,
   `X-Resume-Token`) or the equivalent query parameters. This lets the relay
   reject with a normal HTTP status **before** upgrading, and gives the long-poll
   path identical auth. The **signed body is the connection-key bytes**; the
   canonical string reuses `internal/auth.Compute` exactly: `key \n ts \n nonce`.
2. **`delivery_seq` is per wire-send, assigned by the endpoint**, monotonic per
   connection (session-independent, survives reconnect). It is distinct from the
   delivery.Channel's internal dispatch sequence; the endpoint owns the wire
   sequence and the resume token.
3. **`resume_token` = highest completed (`result`-reported) `delivery_seq`.**
   D-5 says "highest acked delivery sequence"; since at-least-once execution
   turns on *completion*, the relay removes a pending order (and advances the
   token) on `result`, not on bare `ack`. An `ack` marks receipt only. This is
   safe because the agent dedups by idempotency key, so redelivering an
   acked-but-not-resulted order cannot double-execute.
4. **WebSocket `Deliver` is synchronous with a bounded wait.** The endpoint
   writes the `order` frame, then blocks (default 5 s, configurable) for the
   matching `result`, so a live push looks like a synchronous fill to the reused
   delivery.Channel and preserves per-connection ordering. If the socket drops
   or the wait times out, `Deliver` returns a `buffered` result and the order
   stays pending for redelivery on the next resume. Result routing uses only the
   endpoint's lock (never the Channel lock), so there is no cross-lock deadlock.
5. **Long-poll `Deliver` is non-blocking** (queue + return `buffered`); the
   execution outcome is reported asynchronously via POST — inherent to a pull
   transport. The GET returns immediately (204 when the queue is empty) rather
   than holding the request open; holding it open up to a poll timeout is a
   trivial extension and does not change the envelope or guarantees.
6. **Two complementary buffers, no double delivery.** The delivery.Channel
   buffers orders dispatched while the agent is *fully offline* (`Online()`
   false) and replays them on `Channel.Reconnect` (FR-9, reused). The endpoint
   buffers orders *sent on a live socket but not yet resulted* and replays them
   on `attach` (TR-4). An order is only ever in one of the two, so reconnect
   drives both without re-sending anything twice.
7. **`control` (pause/resume) stays server-enforced.** The envelope defines a
   `control` type, but pause/resume continue to be enforced in the relay via the
   delivery.Channel pause gate (FR-11), which is where entries are actually
   gated; a `control` push to the agent is therefore advisory and not required
   for correctness.
8. **TR-8 `SocketTlsConnect` naming.** The shipping MQL5 API has no
   `SocketTlsConnect`; a TLS socket is `SocketConnect` + `SocketTlsHandshake`,
   then `SocketTlsSend`/`SocketTlsRead`. The reference agent uses those (same
   intent: native TLS, no DLL). Documented in `ea/README.md`.
9. **Concurrency-limit on connect.** A transport connection counts against the
   tier limit when its endpoint is first registered with the relay; a reconnect
   reuses the same endpoint and does not consume a second slot. The slot is held
   across short disconnects so resume within the window works.

All test credentials are obvious fakes (`transport-conn-key`, secret `s3cr3t`);
no real secrets are present.
