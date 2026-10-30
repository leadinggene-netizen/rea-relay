// Package relay is the ingestion-to-dispatch orchestrator (the "Service
// backend" actor). It wires authentication (FR-1..5), symbol mapping (FR-6),
// server-side risk resolution (FR-7, §6) and delivery semantics (FR-8..11)
// together, and emits the stable §8 error taxonomy. Stores are in-memory.
package relay

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/leadinggene-netizen/rea-relay/internal/auth"
	"github.com/leadinggene-netizen/rea-relay/internal/connection"
	"github.com/leadinggene-netizen/rea-relay/internal/delivery"
	"github.com/leadinggene-netizen/rea-relay/internal/instruction"
	"github.com/leadinggene-netizen/rea-relay/internal/reason"
	"github.com/leadinggene-netizen/rea-relay/internal/risk"
)

// Config holds tunable windows and limits with documented defaults
// (see BUILD-REPORT.md "assumptions").
type Config struct {
	DedupWindow   time.Duration // FR-4 idempotency suppression window (default 5m)
	SignatureSkew time.Duration // NFR-3 allowed timestamp skew (default 30s)
	RatePerSecond float64       // FR-5 sustained inbound rate per connection (default 10)
	RateBurst     float64       // FR-5 burst capacity (default 20)
	ReplayWindow  time.Duration // FR-9 offline buffer/replay window (default 60s)
}

// DefaultConfig returns the documented defaults.
func DefaultConfig() Config {
	return Config{
		DedupWindow:   5 * time.Minute,
		SignatureSkew: 30 * time.Second,
		RatePerSecond: 10,
		RateBurst:     20,
		ReplayWindow:  60 * time.Second,
	}
}

// Request is one inbound webhook: the raw JSON body plus optional signature
// material presented alongside it (NFR-3).
type Request struct {
	Body      []byte
	Signature *auth.SignatureMaterial
}

// Result is the connection-level outcome returned to the instruction source
// (and mirrored to the audit log). Code is empty on full acceptance.
type Result struct {
	Accepted       bool
	InstructionID  string
	IdempotencyKey string
	Code           reason.Code
	Message        string
	Overall        delivery.Status
	Accounts       []delivery.AccountResult
	Rounded        bool
}

// Relay is the orchestrator. It is safe for concurrent use.
type Relay struct {
	cfg     Config
	store   *connection.Store
	quotes  risk.QuoteSource
	signer  *auth.Signer
	dedup   *auth.Deduper
	limiter *auth.RateLimiter
	audit   *AuditLog
	clock   func() time.Time

	mu       sync.Mutex
	channels map[string]*delivery.Channel // keyed by connection ID
}

// New builds a Relay over the given stores and quote source.
func New(cfg Config, store *connection.Store, quotes risk.QuoteSource, audit *AuditLog) *Relay {
	return &Relay{
		cfg:      cfg,
		store:    store,
		quotes:   quotes,
		signer:   auth.NewSigner(cfg.SignatureSkew),
		dedup:    auth.NewDeduper(cfg.DedupWindow),
		limiter:  auth.NewRateLimiter(cfg.RatePerSecond, cfg.RateBurst),
		audit:    audit,
		clock:    time.Now,
		channels: map[string]*delivery.Channel{},
	}
}

// SetClock overrides the relay's time source and propagates it to auth
// sub-components (tests).
func (r *Relay) SetClock(f func() time.Time) {
	r.mu.Lock()
	r.clock = f
	r.mu.Unlock()
}

// Audit exposes the audit log.
func (r *Relay) Audit() *AuditLog { return r.audit }

// ErrConnectionLimit is returned by RegisterAgent when the tier's concurrency
// budget is exhausted (FR-17, EC-14, TC-X4).
var ErrConnectionLimit = errors.New(string(reason.ConnectionLimit))

// RegisterAgent attaches a terminal-agent transport to a connection, counting
// it against the subscription's concurrent-connection limit (FR-17). It returns
// ErrConnectionLimit when the limit is exceeded, recording connection_limit.
func (r *Relay) RegisterAgent(connID string, t delivery.Transport) (*delivery.Channel, error) {
	if code, ok := r.store.Connect(connID); !ok {
		r.audit.Audit(connID, code, "agent registration refused")
		if code == reason.ConnectionLimit {
			return nil, ErrConnectionLimit
		}
		return nil, errors.New(string(code))
	}
	ch := delivery.NewChannel(connID, t, r.cfg.ReplayWindow, r.audit)
	r.mu.Lock()
	r.channels[connID] = ch
	r.mu.Unlock()
	return ch, nil
}

// Channel returns the delivery channel for a connection, if registered.
func (r *Relay) Channel(connID string) (*delivery.Channel, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ch, ok := r.channels[connID]
	return ch, ok
}

// reject builds a rejected Result and records it to the audit log (§8: never
// silently vanish).
func (r *Relay) reject(connID, instrID, idem string, code reason.Code) Result {
	r.audit.Audit(connID, code, code.Message())
	return Result{InstructionID: instrID, IdempotencyKey: idem, Code: code, Message: code.Message()}
}

// Process runs one inbound instruction through the full pipeline and returns
// the connection-level Result (§8 taxonomy). It never panics on bad input.
func (r *Relay) Process(req Request) Result {
	instrID := newID()

	// 1. Parse (TC-X3 malformed_payload).
	var in instruction.Instruction
	if err := json.Unmarshal(req.Body, &in); err != nil {
		return r.reject("", instrID, "", reason.MalformedPayload)
	}

	// 2. Missing / placeholder key (EC-1, TC-X1).
	if connection.IsPlaceholderKey(in.ConnectionKey) {
		return r.reject("", instrID, "", reason.MissingKey)
	}

	// 3. Lookup (FR-2 unknown_key).
	conn, ok := r.store.Lookup(in.ConnectionKey)
	if !ok {
		return r.reject("", instrID, "", reason.UnknownKey)
	}

	// 4. Expiry (FR-20, EC-15).
	if conn.Expired(r.now()) {
		return r.reject(conn.ID, instrID, "", reason.ExpiredKey)
	}

	// 5. Signature (FR-3, NFR-3, TC-X2).
	if conn.RequireSignature {
		if req.Signature == nil || !r.signer.Verify(conn.Secret, *req.Signature) {
			return r.reject(conn.ID, instrID, "", reason.BadSignature)
		}
	}

	// 6. Schema validation (FR-6; schema_version_unsupported / malformed_payload).
	if verr := in.Validate(); verr != nil {
		return r.reject(conn.ID, instrID, "", verr.Code)
	}

	// 7. Idempotency key: use supplied or derive from the payload hash.
	idem := in.IdempotencyKey
	if idem == "" {
		idem = hashBody(req.Body)
	}

	// 8. Dedup within window (FR-4, EC-8, TC-E5): ack, no second execution.
	if r.dedup.Seen(conn.ID, idem) {
		r.audit.Audit(conn.ID, reason.DuplicateSuppressed, "idempotency key repeated within window")
		return Result{Accepted: true, InstructionID: instrID, IdempotencyKey: idem,
			Code: reason.DuplicateSuppressed, Message: reason.DuplicateSuppressed.Message(),
			Overall: delivery.StatusDuplicate}
	}

	// 9. Rate limit (FR-5, EC-13): over budget we queue (never drop an accepted
	// instruction), recording the pressure rather than shedding.
	if !r.limiter.Allow(conn.ID) {
		r.audit.Audit(conn.ID, reason.RateLimited, "inbound rate limit exceeded; queued")
	}

	// 10. Agent control actions (FR-11).
	switch in.Action {
	case instruction.AgentPause, instruction.AgentResume:
		return r.handleAgentControl(conn, instrID, idem, in.Action)
	}

	// 11. Symbol mapping (FR-6, EC-2, TC-E1).
	spec, ok := conn.MapSymbol(in.Symbol)
	if !ok {
		return r.reject(conn.ID, instrID, idem, reason.SymbolUnmapped)
	}

	ch, ok := r.Channel(conn.ID)
	if !ok {
		// No agent attached: treat as offline — nothing to deliver to.
		return r.reject(conn.ID, instrID, idem, reason.DeliveryExpired)
	}

	if in.Action.IsEntry() {
		return r.processEntry(conn, ch, spec, &in, instrID, idem)
	}
	return r.processNonEntry(conn, ch, spec, &in, instrID, idem)
}

// handleAgentControl applies pause/resume to the connection's channel (FR-11).
func (r *Relay) handleAgentControl(conn *connection.Connection, instrID, idem string, a instruction.Action) Result {
	ch, ok := r.Channel(conn.ID)
	if !ok {
		return r.reject(conn.ID, instrID, idem, reason.DeliveryExpired)
	}
	if a == instruction.AgentPause {
		ch.Pause()
		r.audit.Audit(conn.ID, reason.AgentPaused, "connection paused")
	} else {
		ch.Resume()
	}
	return Result{Accepted: true, InstructionID: instrID, IdempotencyKey: idem, Overall: delivery.StatusSkipped}
}

// processEntry resolves volume/SL/TP per target account (§6), applies the
// spread and account_filter pre-trade gates (FR-15), and dispatches with
// per-connection ordering (FR-8) and fan-out (FR-10).
func (r *Relay) processEntry(conn *connection.Connection, ch *delivery.Channel, spec connection.BrokerSymbolSpec, in *instruction.Instruction, instrID, idem string) Result {
	// Instruction-level stop requirement (EC-3, TC-E2).
	if (in.Sizing.Mode == instruction.CashRisk || in.Sizing.Mode == instruction.PercentBalanceRisk) && in.StopLoss == nil {
		return r.reject(conn.ID, instrID, idem, reason.RiskRequiresStop)
	}

	// Quote + spread gate (FR-15, EC-6, TC-E6).
	q, ok := r.quotes.Quote(spec.BrokerSymbol)
	if !ok {
		return r.reject(conn.ID, instrID, idem, reason.SymbolUnmapped)
	}
	if in.MaxSpread != nil && q.Spread > *in.MaxSpread {
		r.audit.Audit(conn.ID, reason.SpreadExceeded, "spread above max_spread")
		return Result{InstructionID: instrID, IdempotencyKey: idem, Code: reason.SpreadExceeded,
			Message: reason.SpreadExceeded.Message(), Overall: delivery.StatusSkipped}
	}

	var results []delivery.AccountResult
	rounded := false
	for _, acct := range conn.Accounts {
		// account_filter gate (FR-15, EC-7): act only if equity >= threshold.
		if in.AccountFilter != nil && acct.Equity < *in.AccountFilter {
			r.audit.Audit(conn.ID, reason.AccountFilterBlocked, "account equity below account_filter")
			results = append(results, delivery.AccountResult{AccountID: acct.ID, Status: delivery.StatusSkipped, Reason: reason.AccountFilterBlocked})
			continue
		}
		res, rerr := risk.Resolve(in, spec, q, acct.Equity)
		if rerr != nil {
			r.audit.Audit(conn.ID, rerr.Code, rerr.Message)
			results = append(results, delivery.AccountResult{AccountID: acct.ID, Status: delivery.StatusRejected, Reason: rerr.Code})
			continue
		}
		if res.Rounded {
			rounded = true
			r.audit.Audit(conn.ID, reason.VolumeRounded, "resolved volume rounded to lot step")
		}
		order := delivery.ResolvedOrder{
			InstructionID: instrID, IdempotencyKey: idem, ConnectionID: conn.ID,
			Action: in.Action, BrokerSymbol: spec.BrokerSymbol, Lots: res.Lots,
			SLPrice: res.SLPrice, TPPrice: res.TPPrice, Comment: in.Comment,
		}
		if in.Action.IsPending() {
			order.PendingPrice = in.PendingPrice
		}
		out := ch.Dispatch(order, []string{acct.ID}, true)
		results = append(results, resultsFor(out, acct.ID, rounded)...)
	}
	return r.finish(conn.ID, instrID, idem, results, rounded)
}

// processNonEntry dispatches closes/modifies/cancels with no risk math, fanning
// out to all accounts (FR-10, FR-13).
func (r *Relay) processNonEntry(conn *connection.Connection, ch *delivery.Channel, spec connection.BrokerSymbolSpec, in *instruction.Instruction, instrID, idem string) Result {
	order := delivery.ResolvedOrder{
		InstructionID: instrID, IdempotencyKey: idem, ConnectionID: conn.ID,
		Action: in.Action, BrokerSymbol: spec.BrokerSymbol, Comment: in.Comment,
		PartialFraction: in.PartialFraction, CloseVolume: in.CloseVolume,
	}
	// Breakeven encoding: a modify with stop_loss == 0 moves SL to breakeven
	// (FR-12). Pass the zero through so the agent interprets it as breakeven.
	if (in.Action == instruction.ModifyStopsLong || in.Action == instruction.ModifyStopsShort) && in.StopLoss != nil {
		sl := *in.StopLoss
		order.SLPrice = &sl
	}

	accountIDs := make([]string, 0, len(conn.Accounts))
	for _, a := range conn.Accounts {
		accountIDs = append(accountIDs, a.ID)
	}
	out := ch.Dispatch(order, accountIDs, false)
	var results []delivery.AccountResult
	if out.Overall == delivery.StatusBuffered {
		for _, id := range accountIDs {
			results = append(results, delivery.AccountResult{AccountID: id, Status: delivery.StatusBuffered})
		}
	} else {
		results = out.Results
	}
	return r.finish(conn.ID, instrID, idem, results, false)
}

// resultsFor adapts a single-account dispatch outcome into account results,
// synthesizing a buffered marker when the agent was offline (FR-9).
func resultsFor(out delivery.DispatchOutcome, acctID string, rounded bool) []delivery.AccountResult {
	if out.Overall == delivery.StatusBuffered {
		return []delivery.AccountResult{{AccountID: acctID, Status: delivery.StatusBuffered}}
	}
	return out.Results
}

// finish reduces per-account results to a connection-level Result and records
// broker rejections / no-ops to the audit log (FR-16, §8).
func (r *Relay) finish(connID, instrID, idem string, results []delivery.AccountResult, rounded bool) Result {
	overall := delivery.Reduce(results)
	var topCode reason.Code
	for _, res := range results {
		switch res.Status {
		case delivery.StatusRejected:
			if res.Reason == "" {
				res.Reason = reason.BrokerRejected
			}
			r.audit.Audit(connID, res.Reason, "account "+res.AccountID+" rejected ("+res.BrokerCode+")")
		case delivery.StatusNoOp:
			r.audit.Audit(connID, reason.NoMatchingPosition, "account "+res.AccountID+" no matching position")
		}
	}
	// Surface a shared reason when every result carries the same one.
	topCode = sharedReason(results)
	accepted := overall == delivery.StatusFilled || overall == delivery.StatusPartial
	msg := ""
	if topCode != "" {
		msg = topCode.Message()
	}
	return Result{
		Accepted: accepted, InstructionID: instrID, IdempotencyKey: idem,
		Code: topCode, Message: msg, Overall: overall, Accounts: results, Rounded: rounded,
	}
}

// sharedReason returns the reason code common to every non-success result, or
// "" when results are mixed or all succeeded.
func sharedReason(results []delivery.AccountResult) reason.Code {
	var code reason.Code
	set := false
	for _, res := range results {
		switch res.Status {
		case delivery.StatusFilled, delivery.StatusPartial, delivery.StatusDuplicate:
			return "" // at least one success: no single rejection reason
		default:
			c := res.Reason
			if c == "" && res.Status == delivery.StatusRejected {
				c = reason.BrokerRejected
			}
			if c == "" && res.Status == delivery.StatusNoOp {
				c = reason.NoMatchingPosition
			}
			if !set {
				code, set = c, true
			} else if code != c {
				return ""
			}
		}
	}
	return code
}

func (r *Relay) now() time.Time {
	r.mu.Lock()
	f := r.clock
	r.mu.Unlock()
	return f()
}

// --- small helpers ---

func newID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func hashBody(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
