# Terminal Agent (MQL5) — reference deliverable (TR-8)

This is a **reference** MQL5 terminal agent for the rea-relay transport. It is a
WebSocket client built on **native MQL5 TLS sockets — no DLL and no "Allow DLL
imports"** (TR-8). It performs the RFC 6455 upgrade handshake and framing in
MQL5, authenticates per TR-2, receives `order` messages, executes them
(FR-12..16), replies with `ack` + `result`, sends heartbeats (TR-5), and
reconnects with resume (TR-4/TR-6).

> **NOT COMPILED OR TESTED in this build environment.** There is no MetaEditor /
> MetaTrader toolchain in the container, so none of this MQL5 has been compiled
> or run. It is written to be idiomatic and correct-by-construction, but it
> **must be compiled in MetaEditor and tested in a MetaTrader 5 terminal before
> any use**. Treat it as a starting reference, not a verified binary.

## Files

- `TerminalAgent.mq5` — the EA: connect/negotiate, read loop, order dispatch,
  heartbeat, reconnect-with-resume.
- `include/WsClient.mqh` — RFC 6455 client over native TLS sockets (handshake,
  masked client frames, ping/pong, close).
- `include/Hashing.mqh` — Base64 / SHA-1 / HMAC-SHA256 via `CryptEncode` (no
  DLL). `HandshakeSignature` reproduces the relay's `internal/auth.Compute`.
- `include/Json.mqh` — minimal builders + extractor for the flat typed envelope.
- `include/TradeExec.mqh` — order execution (FR-12..16) via `CTrade`.

## Install & build

1. Copy the `ea/` contents into `MQL5/Experts/rea-relay/` inside your terminal's
   data folder (keep the `include/` subfolder next to `TerminalAgent.mq5`).
2. Open `TerminalAgent.mq5` in MetaEditor and compile (F7). Resolve any
   broker/build-specific warnings.
3. In the terminal: **Tools → Options → Expert Advisors**, add the relay host to
   **Allow WebRequest / Allow connections** for the socket host:port
   (MetaTrader gates outbound sockets by host — add `relay.example.com:443`).
   **Do NOT enable "Allow DLL imports"** — this agent needs none.
4. Attach the EA to any chart; set the inputs (host, port, path, connection key,
   optional shared secret).

## Spec deviation to be aware of

§13 TR-8 names `SocketTlsConnect`. The shipping MQL5 API has no function by that
name; a TLS socket is realized with `SocketConnect` followed by
`SocketTlsHandshake`, then `SocketTlsSend` / `SocketTlsRead` /
`SocketTlsReadAvailable`. `WsClient.mqh` uses those. Behavior matches the spec's
intent (native TLS, no DLL); only the function name differs.

## What to verify in MetaEditor / a terminal (not verified here)

Protocol / transport:
- [ ] Compiles cleanly in MetaEditor with no DLL imports enabled.
- [ ] TLS socket connects and the RFC 6455 upgrade succeeds (server returns
      `101`); a `welcome` envelope with `session_id` + `resume_token` is read.
- [ ] Bad/absent signature on a signature-required connection is rejected at
      handshake (`error` envelope with `bad_signature`, socket closed).
- [ ] `HandshakeSignature` matches `internal/auth.Compute` for the same
      `secret`, `connection_key`, `ts`, `nonce` (compare hex against a Go unit).
- [ ] Masked client frames are accepted by the relay; server (unmasked) frames
      are parsed, including 2-byte and 8-byte extended lengths.
- [ ] Ping from the relay is answered with pong; heartbeat every 15 s keeps the
      account online; stopping heartbeats marks it offline server-side.
- [ ] After a forced disconnect, the EA reconnects with exponential backoff and
      presents `resume_token`; the relay redelivers unacked orders once, and the
      agent's idempotency guard prevents a second execution.

Execution (FR-12..16) on a demo account:
- [ ] `open_long` / `open_short` market orders fill at the resolved volume with
      the absolute SL/TP from the envelope (no agent-side risk math).
- [ ] `open_long_pending` / `open_short_pending` place limit/stop orders chosen
      from the pending price vs market.
- [ ] `modify_stops_long/short` moves SL/TP; a `sl_price` of `0` moves SL to the
      position open price (breakeven).
- [ ] `close_long/short` closes fully; `close_*_partial` closes the fraction of
      initial volume (25/34/50); `close_*_volume` closes up to the given volume;
      `close_all` closes both sides of the symbol.
- [ ] Volume outside broker min/max is reported `rejected` with
      `volume_out_of_range`; volume is snapped to the broker lot step.
- [ ] A partial below broker min closes the position fully (EC-11).
- [ ] `max_spread` and `account_filter` gates skip with `spread_exceeded` /
      `account_filter_blocked` and report the skip.
- [ ] Every attempt reports `filled` / `partial` / `rejected` / `skipped` /
      `no_op` with the broker retcode on rejection; no matching position yields
      `no_op` / `no_matching_position`.
