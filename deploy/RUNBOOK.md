# rea-relay — Runtime & Demo Test Runbook

_Date: 2026-10-30 • For the first end-to-end EA ↔ relay test on a demo account._

## Should you spin up a new VPS now? — Staged answer

**Yes for a smoke test, no for a production soak — and know which one you're doing.**

- **Stage A — Smoke test (do this first).** Prove the loop works *once*: TradingView-style webhook → relay → WebSocket → EA → order on a **demo** account → result back. Use the dev relay as-is (in-memory, one hardcoded demo connection, stubbed quotes). Minimum infra: one small VPS for the relay behind TLS; the EA can run on your **local** MT5 demo terminal — MT5 does **not** need to be on the VPS yet.
- **Stage B — Soak / real test.** Only after Stage A is green. Run 24/7, fire real TradingView alerts, host MT5+EA on the VPS, watch fan-out/reconnect/latency over days. **This needs the next build slice first** (see "Blocking gaps").

Deploying a fresh VPS *and* MT5 hosting *and* untested EA↔relay wiring all at once means debugging several unknowns together. Stage A isolates the code from the infra.

## Topology (Stage A)

```
TradingView (or test script)
      │  HTTPS POST /webhook
      ▼
[ VPS ]  Caddy :443 (auto-TLS)  ──reverse_proxy──►  relay :8080 (plain HTTP, in-memory)
      ▲                                                     │  order over WSS
      │  WSS /agent/ws                                      ▼
[ your PC ]  MT5 demo terminal + TerminalAgent EA ──OrderSend──► demo broker
```

TLS lives in Caddy because the EA always does a TLS handshake and the relay serves plain HTTP.

## Artifacts in this bundle
- `relay-linux-amd64`, `relay-windows-amd64.exe` — the relay server (pick your VPS OS).
- `Caddyfile` — reverse proxy with automatic Let's Encrypt TLS.
- `send-test-webhook.ps1` / `.sh` — fire one demo instruction at `/webhook`.

## Blocking gaps (read before Stage B)
The `cmd/relay` binary is a **development entrypoint** (its own header says so). For a smoke test it's fine; for a real soak it is NOT, because:
1. **In-memory state** — every relay restart wipes all sessions/connections.
2. **One hardcoded connection** — `demo-connection-key`, symbol map `XAUUSD → XAUUSD.m` (pip 0.1, lot min 0.01 / max 100 / step 0.01), one demo account. No runtime provisioning / admin API.
3. **Stubbed quotes** — risk sizing uses a fixed `XAUUSD.m` quote (2400.0/2400.2), **not live broker prices**. So for the first test use `sizing.mode = fixed_lots` only; `cash_risk` / `percent_balance*` would size against the stub.
4. **No signature required on the demo connection** — fine for a closed test; turn HMAC on before any public exposure.

→ Stage B prerequisites (next build slice): persistence (DB), runtime connection provisioning, a **live quote feed**, and secret-required connections by default.

## Match the symbol spec to YOUR broker (important)
Your demo broker's gold symbol may be `XAUUSD`, `GOLD`, `XAUUSD.m`, etc., with its own digits/contract size. If the seeded `XAUUSD → XAUUSD.m` mapping or pip/lot specs don't match, you'll get `symbol_unmapped` or wrong sizing. Tell me your broker + the exact symbol and I'll adjust the seed in `cmd/relay/main.go` (or we add config).

---

## Stage A — step by step

### 1. Deploy the relay (VPS)
```bash
# on the VPS (Linux example)
./relay-linux-amd64 -addr 127.0.0.1:8080    # runs in foreground; use systemd/tmux to keep alive
```

### 2. TLS front (VPS)
- Point a subdomain's A record at the VPS IP; open inbound TCP 80 and 443.
- Edit `Caddyfile`: replace `relay.example.com` with your subdomain.
- Install Caddy and run: `caddy run --config Caddyfile` (it fetches a cert automatically).
- Verify: `curl https://relay.example.com/healthz` → `ok`.

### 3. MT5 terminal (your PC, demo account)
- Log into a **demo** account.
- **Tools → Options → Expert Advisors**: tick *Allow Algo Trading*, and add your relay host to *Allow WebRequest for listed URL* (MQL5 socket connections use this allow-list) — e.g. `https://relay.example.com`.
- Put `TerminalAgent.mq5` + the `include\` folder in `MQL5\Experts\`, compile (F7) — already done.
- Attach **TerminalAgent** to any chart. Set inputs:
  - `InpHost = relay.example.com`
  - `InpPort = 443`
  - `InpPath = /agent/ws`
  - `InpConnectionKey = demo-connection-key`
  - `InpSecret = ` (leave empty for the demo connection)
  - `InpMagic = 770001`
- On attach, the EA should connect; watch the **Experts** tab log for the handshake + a `welcome` with `session_id`.

### 4. Fire a test order
```powershell
# from your PC (demo market open; gold trades ~Sun 18:00–Fri 17:00 ET)
.\send-test-webhook.ps1 -RelayUrl "https://relay.example.com" -Action open_long -Lots 0.01
```
Expect: the relay returns a JSON result; within ~1s a 0.01-lot XAUUSD buy appears on the demo account; the EA reports the fill back (visible in the relay log / Experts tab).

### 5. Exercise the behaviors
- `-Action close_long` → closes it.
- Detach/reattach the EA mid-flight → it should reconnect and resume (TR-4/6).
- Stop the relay briefly, fire a webhook, restart → observe offline-buffer/replay within the 60s window.

## Verification checklist
- [ ] `GET /healthz` → `ok` over HTTPS.
- [ ] EA connects: RFC 6455 upgrade returns 101; `welcome` with `session_id` + `resume_token` logged.
- [ ] `open_long` → demo order appears at ~0.01 lot, correct symbol; result reported back.
- [ ] `close_long` → position closed.
- [ ] Reconnect after EA detach/reattach; no duplicate orders (idempotency).
- [ ] Heartbeats keep the socket alive > 1 min idle.
- [ ] Bad `InpConnectionKey` → handshake rejected (`bad`/`unknown key`), no order.

## When Stage A is green
Tell me, and we scope the Stage-B slice: persistence + provisioning + live quotes + HMAC-on, then host MT5 on the VPS (Docker/Wine or Windows VPS) and start the soak.
