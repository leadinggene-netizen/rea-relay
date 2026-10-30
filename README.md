# rea-relay — v1 relay backend (Signal Bridge "Service backend")

A clean-room implementation of the **relay backend** actor from the behavioral
specification (`spec.md §2`): it ingests authenticated trade-instruction
webhooks, authenticates and deduplicates them, maps symbols, resolves
risk/volume **server-side**, and delivers fully-resolved orders to a terminal
agent with at-least-once semantics, per-connection ordering, fan-out,
pause/resume and an offline buffer with resume-token replay.

The **terminal agent** (execution against a broker, spec FR-12..16) is **out of
scope** and is modeled behind the `delivery.Transport` interface with an
in-memory `MockAgent` used only by tests. There is no MetaTrader, no broker, no
wire protocol and no database — all stores are in-memory behind interfaces.

Module path: `github.com/leadinggene-netizen/rea-relay` · Go 1.24.

## Build / test

```
make build   # go build ./...
make vet     # go vet ./...
make test    # go test ./... -count=1
make run     # run the HTTP ingress on :8080
```

## HTTP ingress

`cmd/relay` wires an in-memory demo connection + mock agent behind
`POST /webhook` (JSON instruction body; optional `X-Signature` /
`X-Timestamp` / `X-Nonce` headers) and `GET /healthz`. It is a development
entrypoint, not a production server.

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
| `cmd/relay` | FR-1 runnable HTTPS webhook ingress (HTTP here; TLS terminates upstream). |

## Test coverage

`internal/relay/relay_test.go` implements every §10 case at the relay boundary
(TC-N1..N6, TC-E1..E7, TC-X1..X6) plus EC-10 and EC-15. `internal/risk` and
`internal/auth` carry unit tests for the §6 math and the replay/dedup/rate
limiter. See `BUILD-REPORT.md` for the per-case pass table and the list of
spec assumptions.
