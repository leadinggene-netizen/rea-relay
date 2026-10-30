# Build report — v1 relay backend

Built strictly from `spec.md`. `go vet ./...` clean; `go test ./... -count=1`
passes (32 tests). The terminal agent (FR-12..16) is out of scope and modeled
behind `delivery.Transport`; its behaviors (fills, partials, rejects, no-op,
agent-side dedup, offline) are simulated by `delivery.MockAgent` so the §10
cases can be exercised at the relay boundary.

## §10 test case results

| Case | Test | Result | Note |
|---|---|---|---|
| TC-N1 | `TestTC_N1_MarketLongFixedLots` | PASS | fixed_lots 0.10, absolute SL/TP set (SL below / TP above entry), filled. |
| TC-N2 | `TestTC_N2_CashRisk` | PASS | cash_risk 100, SL 25 pips → 4.00 lots (100 / (25×$1)); SL loss ≈ 100. |
| TC-N3 | `TestTC_N3_ModifyBreakeven` | PASS | modify_stops_long stop=0 → breakeven (SL 0 passed to agent); volume unchanged. |
| TC-N4 | `TestTC_N4_PartialClose` | PASS | 50% of 0.20 → 0.10 closed, 0.10 remains. |
| TC-N5 | `TestTC_N5_FanOutThreeAccounts` | PASS | 3 accounts → 3 independent fills, overall filled. |
| TC-N6 | `TestTC_N6_PauseResume` | PASS | entry during pause skipped `agent_paused`; flows after resume. |
| TC-E1 | `TestTC_E1_SymbolUnmapped` | PASS | unmapped symbol → `symbol_unmapped`, no order. |
| TC-E2 | `TestTC_E2_RiskRequiresStop` | PASS | cash_risk without SL → `risk_requires_stop`. |
| TC-E3 | `TestTC_E3_VolumeOutOfRange` | PASS | target 0.004 < broker min 0.01 → `volume_out_of_range`, no order. |
| TC-E4 | `TestTC_E4_LotStepRounding` | PASS | target 0.117, step 0.01 → 0.12, `volume_rounded` logged. |
| TC-E5 | `TestTC_E5_DuplicateSuppressed` | PASS | repeat idempotency key → single execution, second `duplicate_suppressed` (acked). |
| TC-E6 | `TestTC_E6_SpreadExceeded` | PASS | spread 3.0 > max_spread 2.0 → `spread_exceeded`, no order. |
| TC-E7 | `TestTC_E7_OfflineReplayAndExpiry` | PASS | offline → buffered; reconnect within window replays exactly once (agent dedup); beyond window → `delivery_expired`. |
| TC-X1 | `TestTC_X1_MissingKey` | PASS | blank and placeholder key → `missing_key`, logged, nothing executed. |
| TC-X2 | `TestTC_X2_BadSignature` | PASS | bad HMAC → `bad_signature`; valid HMAC+ts+nonce accepted. |
| TC-X3 | `TestTC_X3_Malformed` | PASS | bad JSON & unknown action → `malformed_payload`; bad version → `schema_version_unsupported`. |
| TC-X4 | `TestTC_X4_ConnectionLimit` | PASS | 4th concurrent connection on a 3-tier → `connection_limit` (at agent registration). |
| TC-X5 | `TestTC_X5_BrokerRejected` | PASS | broker reject → `broker_rejected` with broker code surfaced. |
| TC-X6 | `TestTC_X6_FanOutPartial` | PASS | 3 accounts, 1 rejects → overall `partial`, 2 filled + 1 `broker_rejected`. |
| EC-10 | `TestEC10_NoMatchingPosition` | PASS | close with no open position → no-op, `no_matching_position` logged. |
| EC-15 | `TestEC15_ExpiredKey` | PASS | past-expiry connection → `expired_key` at the UTC day boundary. |

### §10 cases that lean on the mock agent (relay-boundary note)

All execution-side cases (TC-N1/N3/N4/X5/X6, TC-E4/E7, EC-10) are asserted at
the relay boundary: the relay's dispatch decision plus the `MockAgent`'s
simulated broker result. They are genuine for the relay's contract; the real
terminal agent (FR-12..16) would substitute for `MockAgent` unchanged. No §10
case was skipped.

## Spec ambiguities / assumptions and the defaults chosen

1. **`percent_balance_lots` convention (§6).** The spec says lots are "expressed
   directly as lots per the configured convention" without fixing the factor.
   Chosen: `lots = balance × (value/100) × PercentBalanceLotFactor`, with
   `PercentBalanceLotFactor = 0.0001` (so 1% on a 10,000-unit balance → 0.01
   lots). Configurable constant in `internal/risk`.

2. **`partial_fraction` set (§3.2).** Spec gives "e.g. 25/34/50%". Chosen fixed
   set: **25, 34, 50** (whole-number percent); any other value → `malformed_payload`.

3. **Lot-step rounding direction (FR-14 / TC-E4).** TC-E4 allows 0.12 *or* 0.11.
   Chosen **round-half-up to the nearest step** (0.117 → 0.12). A rounded value
   that falls outside [min,max] is rejected `volume_out_of_range`.

4. **Clamp vs reject (§6 / EC-4 / TC-E3).** §6 says "clamp to [min,max] … if clamp
   rejects, emit `volume_out_of_range`." Interpreted per EC-4/TC-E3 as **reject**
   when the target is below min or above max (no silent clamp-to-bound).

5. **Rounding is done server-side (FR-7 vs FR-14).** FR-7 says the agent does no
   risk math; FR-14 attributes lot-step rounding to the agent. Chosen: the relay
   rounds during resolution (FR-7 wins) and logs `volume_rounded`; the mock agent
   fills the already-rounded volume.

6. **Rate-limit window / reason (FR-5, EC-13).** Defaults: **10 instr/s sustained,
   burst 20** per connection (token bucket). EC-13 requires "no loss of
   already-accepted instructions", so over-budget instructions are **queued, not
   dropped**; the relay records a `rate_limited` audit event (this code is not in
   the minimum §8 list — added as an internal audit code only).

7. **Idempotency / dedup window (FR-4).** Default **5 minutes**. If the payload
   carries no `idempotency_key`, one is derived as the SHA-256 of the raw body.

8. **Signature scheme (FR-3, NFR-3).** HMAC-SHA256 over `body\n timestamp\n nonce`;
   default allowed skew **30 s**; nonces are cached and replays rejected.

9. **Offline buffer / replay window (FR-9, EC-9).** Default **60 s**. Resume token
   = the highest delivery sequence the agent has acknowledged; only later
   sequences are replayed; older-than-window entries are dropped `delivery_expired`.

10. **Expiry boundary (FR-20).** "Access ends at the start of the expiry day" →
    cutoff is **midnight UTC of the expiry date**; `now >= cutoff` ⇒ `expired_key`.

11. **Reserved comment delimiters (§3.2).** Disallowed characters: `;`, `|`,
    CR, LF → `malformed_payload`.

12. **`account_filter` semantics (§3.2, FR-15).** Interpreted as "act only if the
    account's equity ≥ threshold", evaluated **per account** on entries; blocked
    accounts are skipped `account_filter_blocked` while others proceed.

13. **`max_spread` gate.** Evaluated once per entry against the symbol quote's
    spread; exceeding it skips the whole entry `spread_exceeded`.

14. **Per-account volume on fan-out.** Because `percent_balance_*` depends on each
    account's balance, entries are resolved and dispatched **per account** (same
    SL/TP prices, possibly different volumes); one instruction may therefore map
    to several delivery sequences.

## Out of scope (not built, per task)

Terminal-agent execution (FR-12..16) beyond the mock, real transport/WebSocket
wire protocol (§5), persistence/DB, scheduled tasks (FR-21), analytics (FR-22),
notifications (FR-23), agent version management (FR-24), and multi-region/NFR
infrastructure concerns.
