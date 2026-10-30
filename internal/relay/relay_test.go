package relay_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/leadinggene-netizen/rea-relay/internal/auth"
	"github.com/leadinggene-netizen/rea-relay/internal/connection"
	"github.com/leadinggene-netizen/rea-relay/internal/delivery"
	"github.com/leadinggene-netizen/rea-relay/internal/reason"
	"github.com/leadinggene-netizen/rea-relay/internal/relay"
	"github.com/leadinggene-netizen/rea-relay/internal/risk"
)

// --- harness ---------------------------------------------------------------

const (
	testKey    = "test-conn-key"
	testSymbol = "GOLD"
	brokerSym  = "XAUUSD.m"
)

// goldSpec is the broker symbol spec used across tests: pip size 0.1, $1 per pip
// per lot, min 0.01, max 100, step 0.01.
var goldSpec = connection.BrokerSymbolSpec{
	BrokerSymbol: brokerSym, PipSize: 0.1, PipValuePerLot: 1.0,
	LotMin: 0.01, LotMax: 100, LotStep: 0.01,
}

type harness struct {
	r     *relay.Relay
	agent *delivery.MockAgent
	conn  *connection.Connection
	store *connection.Store
	audit *relay.AuditLog
	ch    *delivery.Channel
}

type harnessOpts struct {
	accounts   []connection.Account
	requireSig bool
	secret     string
	spread     float64
	expiry     time.Time
	noRegister bool
}

func newHarness(t *testing.T, o harnessOpts) *harness {
	t.Helper()
	if len(o.accounts) == 0 {
		o.accounts = []connection.Account{{ID: "acct-1", Mode: "demo", Equity: 10000}}
	}
	store := connection.NewStore()
	store.AddSubscription(&connection.Subscription{ID: "sub-1", Tier: "pro", MaxConnections: 3})
	conn := &connection.Connection{
		ID: "conn-1", Key: testKey, SubscriptionID: "sub-1",
		RequireSignature: o.requireSig, Secret: o.secret, ExpiryUTC: o.expiry,
		SymbolTable: map[string]connection.BrokerSymbolSpec{testSymbol: goldSpec},
		Accounts:    o.accounts,
	}
	store.AddConnection(conn)

	spread := o.spread
	quotes := risk.MapQuoteSource{brokerSym: {Bid: 2400.0, Ask: 2400.2, Spread: spread}}

	audit := relay.NewAuditLog()
	r := relay.New(relay.DefaultConfig(), store, quotes, audit)

	h := &harness{r: r, conn: conn, store: store, audit: audit}
	if !o.noRegister {
		agent := delivery.NewMockAgent(0.01)
		ch, err := r.RegisterAgent(conn.ID, agent)
		if err != nil {
			t.Fatalf("register agent: %v", err)
		}
		h.agent = agent
		h.ch = ch
	}
	return h
}

func body(t *testing.T, m map[string]any) []byte {
	t.Helper()
	m["schema_version"] = defaultIfMissing(m, "schema_version", 1)
	m["connection_key"] = defaultIfMissing(m, "connection_key", testKey)
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func defaultIfMissing(m map[string]any, k string, def any) any {
	if v, ok := m[k]; ok {
		return v
	}
	return def
}

func f(v float64) *float64 { return &v }

// --- Normal cases ----------------------------------------------------------

// TC-N1: market long, fixed_lots 0.10, SL 20 pips, TP 40 pips -> one order,
// volume 0.10, absolute SL/TP set, filled.
func TestTC_N1_MarketLongFixedLots(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	res := h.r.Process(relay.Request{Body: body(t, map[string]any{
		"action": "open_long", "symbol": testSymbol,
		"sizing":    map[string]any{"mode": "fixed_lots", "value": 0.10},
		"stop_loss": 20.0, "stop_type": "pips",
		"take_profit": 40.0, "target_type": "pips",
	})})
	if !res.Accepted || res.Overall != delivery.StatusFilled {
		t.Fatalf("want accepted filled, got accepted=%v overall=%s code=%s", res.Accepted, res.Overall, res.Code)
	}
	if len(res.Accounts) != 1 || res.Accounts[0].FinalVolume != 0.10 {
		t.Fatalf("want 1 account volume 0.10, got %+v", res.Accounts)
	}
	o, _ := h.agent.LastOrder("acct-1")
	if o.SLPrice == nil || o.TPPrice == nil {
		t.Fatalf("want absolute SL/TP set, got SL=%v TP=%v", o.SLPrice, o.TPPrice)
	}
	// Long: SL below entry (2400.2 - 2.0), TP above (2400.2 + 4.0).
	if !(*o.SLPrice < 2400.2 && *o.TPPrice > 2400.2) {
		t.Fatalf("SL/TP direction wrong: SL=%v TP=%v", *o.SLPrice, *o.TPPrice)
	}
}

// TC-N2: market short, cash_risk 100, SL 25 pips -> volume so SL loss ~= 100.
func TestTC_N2_CashRisk(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	res := h.r.Process(relay.Request{Body: body(t, map[string]any{
		"action": "open_short", "symbol": testSymbol,
		"sizing":    map[string]any{"mode": "cash_risk", "value": 100.0},
		"stop_loss": 25.0, "stop_type": "pips",
	})})
	if res.Overall != delivery.StatusFilled {
		t.Fatalf("want filled, got %s code=%s", res.Overall, res.Code)
	}
	// L = 100 / (25 * 1) = 4.0.
	if got := res.Accounts[0].FinalVolume; got != 4.0 {
		t.Fatalf("want volume 4.0, got %v", got)
	}
}

// TC-N3: modify_stops_long with stop=0 -> SL moved to breakeven on long positions.
func TestTC_N3_ModifyBreakeven(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.agent.SeedPosition("acct-1", brokerSym, "long", 0.20)
	res := h.r.Process(relay.Request{Body: body(t, map[string]any{
		"action": "modify_stops_long", "symbol": testSymbol,
		"stop_loss": 0.0, "stop_type": "price",
	})})
	if res.Overall != delivery.StatusFilled {
		t.Fatalf("want filled, got %s code=%s", res.Overall, res.Code)
	}
	o, _ := h.agent.LastOrder("acct-1")
	if o.SLPrice == nil || *o.SLPrice != 0 {
		t.Fatalf("want breakeven SL=0 passed to agent, got %v", o.SLPrice)
	}
	if h.agent.PositionVolume("acct-1", brokerSym, "long") != 0.20 {
		t.Fatalf("position volume should be unchanged")
	}
}

// TC-N4: close_long_partial 50% on 0.20 -> 0.10 closed, 0.10 remains.
func TestTC_N4_PartialClose(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.agent.SeedPosition("acct-1", brokerSym, "long", 0.20)
	res := h.r.Process(relay.Request{Body: body(t, map[string]any{
		"action": "close_long_partial", "symbol": testSymbol,
		"partial_fraction": 50,
	})})
	if res.Accounts[0].Status != delivery.StatusPartial || res.Accounts[0].FinalVolume != 0.10 {
		t.Fatalf("want 0.10 closed partial, got %+v", res.Accounts[0])
	}
	if got := h.agent.PositionVolume("acct-1", brokerSym, "long"); got != 0.10 {
		t.Fatalf("want 0.10 remaining, got %v", got)
	}
}

// TC-N5: one instruction, 3 accounts -> 3 independent fills, overall filled.
func TestTC_N5_FanOutThreeAccounts(t *testing.T) {
	h := newHarness(t, harnessOpts{accounts: []connection.Account{
		{ID: "a1", Equity: 10000}, {ID: "a2", Equity: 10000}, {ID: "a3", Equity: 10000},
	}})
	res := h.r.Process(relay.Request{Body: body(t, map[string]any{
		"action": "open_long", "symbol": testSymbol,
		"sizing": map[string]any{"mode": "fixed_lots", "value": 0.10},
	})})
	if res.Overall != delivery.StatusFilled || len(res.Accounts) != 3 {
		t.Fatalf("want 3 fills overall filled, got overall=%s n=%d", res.Overall, len(res.Accounts))
	}
	for _, a := range res.Accounts {
		if a.Status != delivery.StatusFilled {
			t.Fatalf("account %s not filled: %+v", a.AccountID, a)
		}
	}
}

// TC-N6: agent_pause then entry skipped agent_paused; after resume entries flow.
func TestTC_N6_PauseResume(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.r.Process(relay.Request{Body: body(t, map[string]any{"action": "agent_pause"})})

	paused := h.r.Process(relay.Request{Body: body(t, map[string]any{
		"action": "open_long", "symbol": testSymbol,
		"sizing":          map[string]any{"mode": "fixed_lots", "value": 0.10},
		"idempotency_key": "n6-while-paused",
	})})
	if paused.Overall != delivery.StatusSkipped || paused.Code != reason.AgentPaused {
		t.Fatalf("want skipped agent_paused, got overall=%s code=%s", paused.Overall, paused.Code)
	}

	h.r.Process(relay.Request{Body: body(t, map[string]any{"action": "agent_resume"})})
	flowed := h.r.Process(relay.Request{Body: body(t, map[string]any{
		"action": "open_long", "symbol": testSymbol,
		"sizing":          map[string]any{"mode": "fixed_lots", "value": 0.10},
		"idempotency_key": "n6-after-resume",
	})})
	if flowed.Overall != delivery.StatusFilled {
		t.Fatalf("want filled after resume, got %s", flowed.Overall)
	}
}

// --- Edge cases ------------------------------------------------------------

// TC-E1: symbol not in map -> symbol_unmapped, no order.
func TestTC_E1_SymbolUnmapped(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	res := h.r.Process(relay.Request{Body: body(t, map[string]any{
		"action": "open_long", "symbol": "EURUSD",
		"sizing": map[string]any{"mode": "fixed_lots", "value": 0.10},
	})})
	if res.Code != reason.SymbolUnmapped || res.Accepted {
		t.Fatalf("want symbol_unmapped reject, got code=%s accepted=%v", res.Code, res.Accepted)
	}
}

// TC-E2: cash_risk with no SL -> risk_requires_stop.
func TestTC_E2_RiskRequiresStop(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	res := h.r.Process(relay.Request{Body: body(t, map[string]any{
		"action": "open_long", "symbol": testSymbol,
		"sizing": map[string]any{"mode": "cash_risk", "value": 100.0},
	})})
	if res.Code != reason.RiskRequiresStop {
		t.Fatalf("want risk_requires_stop, got %s", res.Code)
	}
}

// TC-E3: resolved volume 0.004 with broker min 0.01 -> volume_out_of_range.
func TestTC_E3_VolumeOutOfRange(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	res := h.r.Process(relay.Request{Body: body(t, map[string]any{
		"action": "open_long", "symbol": testSymbol,
		"sizing": map[string]any{"mode": "fixed_lots", "value": 0.004},
	})})
	if res.Code != reason.VolumeOutOfRange || res.Overall == delivery.StatusFilled {
		t.Fatalf("want volume_out_of_range no fill, got code=%s overall=%s", res.Code, res.Overall)
	}
}

// TC-E4: lot step 0.01, target 0.117 -> executes 0.12, logs volume_rounded.
func TestTC_E4_LotStepRounding(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	res := h.r.Process(relay.Request{Body: body(t, map[string]any{
		"action": "open_long", "symbol": testSymbol,
		"sizing": map[string]any{"mode": "fixed_lots", "value": 0.117},
	})})
	if res.Overall != delivery.StatusFilled {
		t.Fatalf("want filled, got %s", res.Overall)
	}
	if res.Accounts[0].FinalVolume != 0.12 {
		t.Fatalf("want rounded volume 0.12, got %v", res.Accounts[0].FinalVolume)
	}
	if !res.Rounded || h.audit.Count(reason.VolumeRounded) == 0 {
		t.Fatalf("want volume_rounded flagged+logged, rounded=%v logged=%d", res.Rounded, h.audit.Count(reason.VolumeRounded))
	}
}

// TC-E5: duplicate idempotency key within window -> single execution, second
// duplicate_suppressed.
func TestTC_E5_DuplicateSuppressed(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	b := body(t, map[string]any{
		"action": "open_long", "symbol": testSymbol,
		"sizing":          map[string]any{"mode": "fixed_lots", "value": 0.10},
		"idempotency_key": "idem-1",
	})
	first := h.r.Process(relay.Request{Body: b})
	second := h.r.Process(relay.Request{Body: b})
	if first.Overall != delivery.StatusFilled {
		t.Fatalf("first should fill, got %s", first.Overall)
	}
	if second.Code != reason.DuplicateSuppressed || !second.Accepted {
		t.Fatalf("second should be acked duplicate_suppressed, got code=%s accepted=%v", second.Code, second.Accepted)
	}
}

// TC-E6: spread 3.0 with max_spread 2.0 -> spread_exceeded, no order.
func TestTC_E6_SpreadExceeded(t *testing.T) {
	h := newHarness(t, harnessOpts{spread: 3.0})
	res := h.r.Process(relay.Request{Body: body(t, map[string]any{
		"action": "open_long", "symbol": testSymbol,
		"sizing":     map[string]any{"mode": "fixed_lots", "value": 0.10},
		"max_spread": 2.0,
	})})
	if res.Code != reason.SpreadExceeded || res.Overall != delivery.StatusSkipped {
		t.Fatalf("want spread_exceeded skip, got code=%s overall=%s", res.Code, res.Overall)
	}
}

// TC-E7: agent offline then back within window -> buffered order replays once;
// beyond window -> delivery_expired.
func TestTC_E7_OfflineReplayAndExpiry(t *testing.T) {
	// Part A: replay within window, exactly once.
	h := newHarness(t, harnessOpts{})
	now := time.Unix(1_700_000_000, 0).UTC()
	clk := now
	h.ch.SetClock(func() time.Time { return clk })
	h.agent.SetOnline(false)

	res := h.r.Process(relay.Request{Body: body(t, map[string]any{
		"action": "open_long", "symbol": testSymbol,
		"sizing":          map[string]any{"mode": "fixed_lots", "value": 0.10},
		"idempotency_key": "idem-off-1",
	})})
	if res.Overall != delivery.StatusBuffered {
		t.Fatalf("want buffered while offline, got %s", res.Overall)
	}
	// Come back online within the window and replay.
	h.agent.SetOnline(true)
	clk = now.Add(5 * time.Second)
	rr := h.ch.Reconnect(0)
	if len(rr.Replayed) != 1 || rr.Replayed[0].Overall != delivery.StatusFilled {
		t.Fatalf("want 1 replayed fill, got %+v", rr.Replayed)
	}
	// A second reconnect must not re-execute (agent dedup => exactly-once).
	rr2 := h.ch.Reconnect(0)
	if len(rr2.Replayed) != 0 {
		t.Fatalf("second reconnect should replay nothing (buffer drained), got %+v", rr2.Replayed)
	}
	if n := h.agent.ExecCount("idem-off-1", "acct-1"); n != 1 {
		t.Fatalf("want exactly-once execution, got %d", n)
	}

	// Part B: beyond the window -> delivery_expired.
	h2 := newHarness(t, harnessOpts{})
	base := time.Unix(1_700_000_000, 0).UTC()
	c2 := base
	h2.ch.SetClock(func() time.Time { return c2 })
	h2.agent.SetOnline(false)
	h2.r.Process(relay.Request{Body: body(t, map[string]any{
		"action": "open_long", "symbol": testSymbol,
		"sizing":          map[string]any{"mode": "fixed_lots", "value": 0.10},
		"idempotency_key": "idem-off-2",
	})})
	h2.agent.SetOnline(true)
	c2 = base.Add(5 * time.Minute) // well beyond 60s replay window
	rr3 := h2.ch.Reconnect(0)
	if len(rr3.Expired) != 1 {
		t.Fatalf("want 1 expired delivery, got %+v", rr3.Expired)
	}
	if h2.audit.Count(reason.DeliveryExpired) == 0 {
		t.Fatalf("want delivery_expired logged")
	}
}

// --- Error cases -----------------------------------------------------------

// TC-X1: missing/placeholder key -> missing_key, nothing executed.
func TestTC_X1_MissingKey(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	for _, k := range []string{"", "REPLACE_ME"} {
		res := h.r.Process(relay.Request{Body: body(t, map[string]any{
			"connection_key": k, "action": "open_long", "symbol": testSymbol,
			"sizing": map[string]any{"mode": "fixed_lots", "value": 0.10},
		})})
		if res.Code != reason.MissingKey {
			t.Fatalf("key %q: want missing_key, got %s", k, res.Code)
		}
	}
	if h.audit.Count(reason.MissingKey) == 0 {
		t.Fatalf("missing_key must be logged")
	}
}

// TC-X2: bad signature on a signature-required connection -> bad_signature.
func TestTC_X2_BadSignature(t *testing.T) {
	h := newHarness(t, harnessOpts{requireSig: true, secret: "s3cr3t"})
	b := body(t, map[string]any{
		"action": "open_long", "symbol": testSymbol,
		"sizing": map[string]any{"mode": "fixed_lots", "value": 0.10},
	})
	// Bad signature.
	bad := h.r.Process(relay.Request{Body: b, Signature: &auth.SignatureMaterial{
		Signature: "deadbeef", Timestamp: time.Now().Unix(), Nonce: "n1", Body: b,
	}})
	if bad.Code != reason.BadSignature {
		t.Fatalf("want bad_signature, got %s", bad.Code)
	}
	// Good signature accepted.
	ts := time.Now().Unix()
	good := h.r.Process(relay.Request{Body: b, Signature: &auth.SignatureMaterial{
		Signature: auth.Compute("s3cr3t", b, ts, "n2"), Timestamp: ts, Nonce: "n2", Body: b,
	}})
	if good.Overall != delivery.StatusFilled {
		t.Fatalf("valid signature should fill, got %s code=%s", good.Overall, good.Code)
	}
}

// TC-X3: malformed payload / unknown action / bad schema version.
func TestTC_X3_Malformed(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	// Invalid JSON.
	if res := h.r.Process(relay.Request{Body: []byte("{not json")}); res.Code != reason.MalformedPayload {
		t.Fatalf("want malformed_payload for bad JSON, got %s", res.Code)
	}
	// Unknown action.
	if res := h.r.Process(relay.Request{Body: body(t, map[string]any{"action": "teleport", "symbol": testSymbol})}); res.Code != reason.MalformedPayload {
		t.Fatalf("want malformed_payload for unknown action, got %s", res.Code)
	}
	// Unsupported schema version.
	if res := h.r.Process(relay.Request{Body: body(t, map[string]any{"schema_version": 99, "action": "open_long", "symbol": testSymbol})}); res.Code != reason.SchemaVersionUnsupported {
		t.Fatalf("want schema_version_unsupported, got %s", res.Code)
	}
}

// TC-X4: 4th concurrent connection on a 3-connection tier -> connection_limit.
func TestTC_X4_ConnectionLimit(t *testing.T) {
	store := connection.NewStore()
	store.AddSubscription(&connection.Subscription{ID: "sub-1", Tier: "pro", MaxConnections: 3})
	for i, id := range []string{"c1", "c2", "c3", "c4"} {
		store.AddConnection(&connection.Connection{ID: id, Key: "k" + id, SubscriptionID: "sub-1",
			SymbolTable: map[string]connection.BrokerSymbolSpec{testSymbol: goldSpec}})
		_ = i
	}
	r := relay.New(relay.DefaultConfig(), store, risk.MapQuoteSource{}, relay.NewAuditLog())
	for _, id := range []string{"c1", "c2", "c3"} {
		if _, err := r.RegisterAgent(id, delivery.NewMockAgent(0.01)); err != nil {
			t.Fatalf("connection %s should register, got %v", id, err)
		}
	}
	_, err := r.RegisterAgent("c4", delivery.NewMockAgent(0.01))
	if err != relay.ErrConnectionLimit {
		t.Fatalf("want ErrConnectionLimit on 4th, got %v", err)
	}
	if r.Audit().Count(reason.ConnectionLimit) == 0 {
		t.Fatalf("connection_limit must be logged")
	}
}

// TC-X5: broker rejects order -> broker_rejected with broker code surfaced.
func TestTC_X5_BrokerRejected(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.agent.ForceReject("acct-1", "BROKER-10004")
	res := h.r.Process(relay.Request{Body: body(t, map[string]any{
		"action": "open_long", "symbol": testSymbol,
		"sizing": map[string]any{"mode": "fixed_lots", "value": 0.10},
	})})
	if res.Overall != delivery.StatusRejected || res.Code != reason.BrokerRejected {
		t.Fatalf("want broker_rejected, got overall=%s code=%s", res.Overall, res.Code)
	}
	if res.Accounts[0].BrokerCode != "BROKER-10004" {
		t.Fatalf("want broker code surfaced, got %q", res.Accounts[0].BrokerCode)
	}
}

// TC-X6: fan-out where 1 of 3 accounts rejects -> overall partial, two filled +
// one broker_rejected.
func TestTC_X6_FanOutPartial(t *testing.T) {
	h := newHarness(t, harnessOpts{accounts: []connection.Account{
		{ID: "a1", Equity: 10000}, {ID: "a2", Equity: 10000}, {ID: "a3", Equity: 10000},
	}})
	h.agent.ForceReject("a3", "BROKER-10019")
	res := h.r.Process(relay.Request{Body: body(t, map[string]any{
		"action": "open_long", "symbol": testSymbol,
		"sizing": map[string]any{"mode": "fixed_lots", "value": 0.10},
	})})
	if res.Overall != delivery.StatusPartial {
		t.Fatalf("want overall partial, got %s", res.Overall)
	}
	var filled, rejected int
	for _, a := range res.Accounts {
		switch a.Status {
		case delivery.StatusFilled:
			filled++
		case delivery.StatusRejected:
			rejected++
			if a.BrokerCode != "BROKER-10019" {
				t.Fatalf("want broker code on rejection, got %q", a.BrokerCode)
			}
		}
	}
	if filled != 2 || rejected != 1 {
		t.Fatalf("want 2 filled 1 rejected, got filled=%d rejected=%d", filled, rejected)
	}
}

// --- extra coverage (spec EC-10, EC-15) ------------------------------------

// EC-10: modify/close with no matching position -> no-op logged.
func TestEC10_NoMatchingPosition(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	res := h.r.Process(relay.Request{Body: body(t, map[string]any{
		"action": "close_long", "symbol": testSymbol,
	})})
	if res.Overall != delivery.StatusNoOp || res.Code != reason.NoMatchingPosition {
		t.Fatalf("want no_op/no_matching_position, got overall=%s code=%s", res.Overall, res.Code)
	}
}

// EC-15 / FR-20: expired connection -> expired_key at the UTC day boundary.
func TestEC15_ExpiredKey(t *testing.T) {
	// Expiry date yesterday => cutoff already passed.
	exp := time.Now().UTC().AddDate(0, 0, -1)
	h := newHarness(t, harnessOpts{expiry: exp})
	res := h.r.Process(relay.Request{Body: body(t, map[string]any{
		"action": "open_long", "symbol": testSymbol,
		"sizing": map[string]any{"mode": "fixed_lots", "value": 0.10},
	})})
	if res.Code != reason.ExpiredKey {
		t.Fatalf("want expired_key, got %s", res.Code)
	}
}
