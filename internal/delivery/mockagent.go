package delivery

import (
	"sync"

	"github.com/leadinggene-netizen/rea-relay/internal/instruction"
	"github.com/leadinggene-netizen/rea-relay/internal/reason"
)

// MockAgent is an in-memory Transport that simulates a terminal agent for tests
// (the real agent, FR-12..16, is out of scope). It models open positions per
// account/symbol/side, agent-side idempotency dedup (FR-8), forced broker
// rejections (TC-X5/X6), and no-matching-position no-ops (EC-10). It is safe for
// concurrent use.
type MockAgent struct {
	mu     sync.Mutex
	online bool

	// positions[accountID][brokerSymbol][side] = open volume.
	positions map[string]map[string]map[string]float64
	// initialVolume tracks a position's opening volume for partial-close math.
	initialVolume map[string]map[string]map[string]float64

	// rejectAccounts forces a broker rejection for specific accounts.
	rejectAccounts map[string]string // accountID -> broker code
	// brokerMin is the smallest volume the simulated broker will close/open.
	brokerMin float64

	// executed caches results by idempotency key for agent-side dedup.
	executed map[string]AccountResult
	// lastOrder records the most recent order delivered per account (tests).
	lastOrder map[string]ResolvedOrder
	// execCount counts non-deduped executions per idempotency key (tests).
	execCount map[string]int
}

// NewMockAgent returns an online mock agent with the given broker minimum lot.
func NewMockAgent(brokerMin float64) *MockAgent {
	return &MockAgent{
		online:         true,
		positions:      map[string]map[string]map[string]float64{},
		initialVolume:  map[string]map[string]map[string]float64{},
		rejectAccounts: map[string]string{},
		brokerMin:      brokerMin,
		executed:       map[string]AccountResult{},
		lastOrder:      map[string]ResolvedOrder{},
		execCount:      map[string]int{},
	}
}

// LastOrder returns the most recent order delivered to an account (tests).
func (m *MockAgent) LastOrder(accountID string) (ResolvedOrder, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.lastOrder[accountID]
	return o, ok
}

// ExecCount returns how many times an idempotency key actually executed (i.e.
// was not suppressed by agent dedup); used to prove at-least-once + dedup =
// effectively exactly-once (FR-8).
func (m *MockAgent) ExecCount(idempotencyKey, accountID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.execCount[idempotencyKey+"\x00"+accountID]
}

// SetOnline toggles the agent's channel (FR-9 offline/replay testing).
func (m *MockAgent) SetOnline(v bool) { m.mu.Lock(); m.online = v; m.mu.Unlock() }

// Online implements Transport.
func (m *MockAgent) Online() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.online }

// ForceReject makes the account reject the next executions with brokerCode
// (TC-X5, TC-X6).
func (m *MockAgent) ForceReject(accountID, brokerCode string) {
	m.mu.Lock()
	m.rejectAccounts[accountID] = brokerCode
	m.mu.Unlock()
}

// SeedPosition pre-opens a position (for close/modify tests).
func (m *MockAgent) SeedPosition(accountID, brokerSymbol, side string, volume float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensure(accountID, brokerSymbol)
	m.positions[accountID][brokerSymbol][side] = volume
	m.initialVolume[accountID][brokerSymbol][side] = volume
}

// PositionVolume reports the open volume for an account/symbol/side (tests).
func (m *MockAgent) PositionVolume(accountID, brokerSymbol, side string) float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.positions[accountID][brokerSymbol]; s != nil {
		return s[side]
	}
	return 0
}

func (m *MockAgent) ensure(accountID, brokerSymbol string) {
	if m.positions[accountID] == nil {
		m.positions[accountID] = map[string]map[string]float64{}
		m.initialVolume[accountID] = map[string]map[string]float64{}
	}
	if m.positions[accountID][brokerSymbol] == nil {
		m.positions[accountID][brokerSymbol] = map[string]float64{}
		m.initialVolume[accountID][brokerSymbol] = map[string]float64{}
	}
}

// Deliver implements Transport: it executes one resolved order on one account,
// deduping by idempotency key (FR-8) so a replayed delivery runs at most once.
func (m *MockAgent) Deliver(accountID string, order ResolvedOrder) AccountResult {
	m.mu.Lock()
	defer m.mu.Unlock()

	dedupKey := order.IdempotencyKey + "\x00" + accountID
	if prev, ok := m.executed[dedupKey]; ok {
		prev.Deduped = true
		return prev
	}

	m.lastOrder[accountID] = order
	m.execCount[dedupKey]++
	res := m.execute(accountID, order)
	m.executed[dedupKey] = res
	return res
}

// execute performs the simulated broker action (caller holds the lock).
func (m *MockAgent) execute(accountID string, order ResolvedOrder) AccountResult {
	if code, bad := m.rejectAccounts[accountID]; bad {
		return AccountResult{AccountID: accountID, Status: StatusRejected, BrokerCode: code, Reason: reason.BrokerRejected}
	}
	m.ensure(accountID, order.BrokerSymbol)
	sides := m.positions[accountID][order.BrokerSymbol]
	initial := m.initialVolume[accountID][order.BrokerSymbol]

	switch order.Action {
	case instruction.OpenLong, instruction.OpenLongPending:
		sides["long"] += order.Lots
		initial["long"] = order.Lots
		return AccountResult{AccountID: accountID, Status: StatusFilled, FinalVolume: order.Lots}
	case instruction.OpenShort, instruction.OpenShortPending:
		sides["short"] += order.Lots
		initial["short"] = order.Lots
		return AccountResult{AccountID: accountID, Status: StatusFilled, FinalVolume: order.Lots}

	case instruction.FlipLong:
		sides["short"] = 0 // close opposite side, then open
		sides["long"] += order.Lots
		initial["long"] = order.Lots
		return AccountResult{AccountID: accountID, Status: StatusFilled, FinalVolume: order.Lots}
	case instruction.FlipShort:
		sides["long"] = 0 // close opposite side, then open
		sides["short"] += order.Lots
		initial["short"] = order.Lots
		return AccountResult{AccountID: accountID, Status: StatusFilled, FinalVolume: order.Lots}

	case instruction.ModifyStopsLong:
		return m.modify(accountID, sides, "long")
	case instruction.ModifyStopsShort:
		return m.modify(accountID, sides, "short")

	case instruction.CloseLong:
		return m.closeFull(accountID, sides, "long")
	case instruction.CloseShort:
		return m.closeFull(accountID, sides, "short")
	case instruction.CloseAll:
		return m.closeAll(accountID, sides)

	case instruction.CloseLongPartial:
		return m.closePartial(accountID, sides, initial, "long", order.PartialFraction)
	case instruction.CloseShortPartial:
		return m.closePartial(accountID, sides, initial, "short", order.PartialFraction)

	case instruction.CloseLongVolume:
		return m.closeVolume(accountID, sides, "long", order.CloseVolume)
	case instruction.CloseShortVolume:
		return m.closeVolume(accountID, sides, "short", order.CloseVolume)

	case instruction.CancelPendingLong, instruction.CancelPendingShort:
		// No pending-order model: treat as no-op success if none to cancel.
		return AccountResult{AccountID: accountID, Status: StatusNoOp, Reason: reason.NoMatchingPosition}
	}
	return AccountResult{AccountID: accountID, Status: StatusNoOp, Reason: reason.NoMatchingPosition}
}

func (m *MockAgent) modify(accountID string, sides map[string]float64, side string) AccountResult {
	if sides[side] <= 0 {
		return AccountResult{AccountID: accountID, Status: StatusNoOp, Reason: reason.NoMatchingPosition}
	}
	// SL moved (incl. breakeven when SLPrice encoded as 0): position volume unchanged.
	return AccountResult{AccountID: accountID, Status: StatusFilled, FinalVolume: sides[side]}
}

func (m *MockAgent) closeFull(accountID string, sides map[string]float64, side string) AccountResult {
	v := sides[side]
	if v <= 0 {
		return AccountResult{AccountID: accountID, Status: StatusNoOp, Reason: reason.NoMatchingPosition}
	}
	sides[side] = 0
	return AccountResult{AccountID: accountID, Status: StatusFilled, FinalVolume: v}
}

func (m *MockAgent) closeAll(accountID string, sides map[string]float64) AccountResult {
	total := sides["long"] + sides["short"]
	if total <= 0 {
		return AccountResult{AccountID: accountID, Status: StatusNoOp, Reason: reason.NoMatchingPosition}
	}
	sides["long"], sides["short"] = 0, 0
	return AccountResult{AccountID: accountID, Status: StatusFilled, FinalVolume: total}
}

func (m *MockAgent) closePartial(accountID string, sides, initial map[string]float64, side string, frac *instruction.PartialFraction) AccountResult {
	open := sides[side]
	if open <= 0 {
		return AccountResult{AccountID: accountID, Status: StatusNoOp, Reason: reason.NoMatchingPosition}
	}
	if frac == nil {
		return AccountResult{AccountID: accountID, Status: StatusRejected, Reason: reason.NoMatchingPosition}
	}
	closeVol := round2(initial[side] * float64(*frac) / 100.0)
	if closeVol > open {
		closeVol = open
	}
	// EC-11: if the fraction is below the broker minimum, close fully and flag.
	if closeVol < m.brokerMin {
		v := open
		sides[side] = 0
		return AccountResult{AccountID: accountID, Status: StatusFilled, FinalVolume: v, Reason: reason.VolumeRounded}
	}
	sides[side] = round2(open - closeVol)
	return AccountResult{AccountID: accountID, Status: StatusPartial, FinalVolume: closeVol}
}

func (m *MockAgent) closeVolume(accountID string, sides map[string]float64, side string, vol *float64) AccountResult {
	open := sides[side]
	if open <= 0 {
		return AccountResult{AccountID: accountID, Status: StatusNoOp, Reason: reason.NoMatchingPosition}
	}
	if vol == nil {
		return AccountResult{AccountID: accountID, Status: StatusRejected, Reason: reason.NoMatchingPosition}
	}
	closeVol := *vol
	if closeVol >= open {
		sides[side] = 0
		return AccountResult{AccountID: accountID, Status: StatusFilled, FinalVolume: open}
	}
	sides[side] = round2(open - closeVol)
	return AccountResult{AccountID: accountID, Status: StatusPartial, FinalVolume: closeVol}
}

func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}
