# rea-relay — v1 relay backend (Signal Bridge "Service backend")

A clean-room implementation of the **relay backend** actor from the behavioral
specification (`spec.md §2`): it ingests authenticated trade-instruction
webhooks, authenticates and deduplicates them, maps symbols, resolves
risk/volume **server-side**, and delivers fully-resolved orders to a terminal
agent with at-least-once semantics, per-connection ordering, fan-out,
pause/resume and an offline buffer with resume-token replay.

As of the **v2 transport slice** (spec §13) the relay now speaks a **real wire
protocol**: a hand-rolled, dependency-free RFC 6455 **WebSocket** endpoint
(primary) and an HTTP **long-poll** fallback, both carrying the same typed JSON
envelope and the same auth + at-least-once + idempotency guarantees, slotted
behind the existing `delivery.Transport` so delivery semantics are reused rather
than duplicated. A **reference MQL5 terminal agent** (execution against a broker,
FR-12..16) lives under `ea/` — it is a native-TLS-socket WebSocket client with
no DLL imports, written as a reference and **not compiled/tested here** (see
`ea/README.md`). The in-memory `MockAgent` remains the test stand-in for the
delivery layer. All relay stores are in-memory behind interfaces.

Module path: `github.com/leadinggene-netizen/rea-relay` · Go 1.24.

## Build / test

```
make build   # go build ./...
make vet     # go vet ./...
make test    # go test ./... -count=1
make run     # run the HTTP ingress on :8080
```

## HTTP ingress

`cmd/relay` wires an in-memory demo connection behind `POST /webhook` (JSON
instruction body; optional `X-Signature` / `X-Timestamp` / `X-Nonce` headers)
and `GET /healthz`, plus the real transport endpoints: `GET /agent/ws` (the
WebSocket upgrade, TR-1..TR-6) and `GET|POST /agent/longpoll` (the fallback,
TR-7). A terminal agent connects to one of those and is registered with the
relay on connect; webhooks then dispatch to it. It is a development entrypoint,
not a production server.

## Package → spec mapping

| Package | Spec coverage |
|---|---|
| `internal/reason` | §8 error taxonomy — the stable machine reason codes + human messages. |
| `internal/instruction` | §3.2 instruction schema and enums; `Validate` (FR-6) → `malformed_payload` / `schema_version_unsupported`; reserved comment delimiters. |
| `internal/auth` | FR-3 HMAC shared-secret signatures with timestamp+nonce replay protection (NFR-3); FR-4 idempotency dedup window; FR-5 per-connection rate limiting. |
| `internal/connection` | §4 Connection/Subscription/Account/SymbolMap; FR-2 key lookup; FR-6 symbol mapping (EC-2 case-sensitive); FR-17 per-tier concurrency (EC-14); FR-18 portable keys; FR-20 UTC expiry boundary (EC-15). |
| `internal/risk` | FR-7 + §6 server-side volume resolution (fixed_lots, cash_risk, percent_balance_lots, percent_balance_risk), broker min/max clamp → `volume_out_of_range` (EC-4), lot-step rounding → `volume_rounded` (EC-5), absolute SL/TP price computation, `risk_requires_stop` (EC-3). Quotes back the spread gate. |
| `internal/delivery` | FR-8 at-least-once + per-connection ordering; FR-9 offline buffer + resume-token replay (EC-9 `delivery_expired`); FR-10 fan-out with independent per-account results (EC-12); FR-11 pause/resume. `Transport` is the agent boundary (FR-12..16 out of scope); `MockAgent` simulates fills/partials/rejects/no-op/dedup for tests. |
| `internal/relay` | The orchestrator: ingestion pipeline (FR-1), ordered auth/validate/dedup/rate steps, symbol mapping (FR-6), pre-trade gates `spread_exceeded`/`account_filter_blocked` (FR-15, EC-6/7), risk dispatch, and the append-only `AuditLog` (§4 AuditEvent, §8 "never silently vanish"). |
| `internal/transport` | §13 real transport. `ws.go` a stdlib RFC 6455 server (Hijack upgrade, Sec-WebSocket-Accept, masked frames, ping/pong/close) — TR-1. `endpoint.go` the per-connection WebSocket `delivery.Transport`: handshake/session + `resume_token` (TR-2), typed envelope (TR-3), at-least-once with monotonic `delivery_seq`, agent acks, redelivery of unacked orders on reconnect within the 60 s window (TR-4), heartbeat → offline (TR-5), reconnect/resume with `delivery_expired` beyond the window (TR-6). `longpoll.go` the HTTP long-poll fallback `delivery.Transport` with the identical envelope + guarantees (TR-7). `server.go` auth (reusing `internal/auth`) + wiring. The MQL5 agent (TR-8) is under `ea/`. |
| `ea/` | TR-8 reference MQL5 terminal agent: native-TLS-socket WebSocket client (no DLL), RFC 6455 handshake/framing, auth, order execution FR-12..16, ack/result, heartbeat, reconnect-with-resume. Reference only — compile/test in MetaEditor, **not verified here**. |
| `cmd/relay` | FR-1 runnable HTTPS webhook ingress + TR-1/TR-7 transport endpoints (HTTP here; TLS terminates upstream). |

## Test coverage

`internal/relay/relay_test.go` implements every §10 case at the relay boundary
(TC-N1..N6, TC-E1..E7, TC-X1..X6) plus EC-10 and EC-15. `internal/risk` and
`internal/auth` carry unit tests for the §6 math and the replay/dedup/rate
limiter. `internal/transport` drives the WebSocket and long-poll endpoints with
a hand-rolled stdlib WS client (raw `net.Conn` + `crypto/sha1`). See
`BUILD-REPORT.md` for the §10 per-case pass table and spec assumptions, and
`TRANSPORT-REPORT.md` for the TR-1..TR-8 coverage table and transport
assumptions.
