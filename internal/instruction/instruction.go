// Package instruction defines the relay's instruction schema (spec §3.2) and
// its validation rules (FR-6, §8 malformed_payload / schema_version_unsupported).
package instruction

import (
	"strings"

	"github.com/leadinggene-netizen/rea-relay/internal/reason"
)

// SupportedSchemaVersion is the only schema version this build accepts.
// A payload with any other value is rejected with schema_version_unsupported.
const SupportedSchemaVersion = 1

// Action is the instruction verb (spec §3.2 enum).
type Action string

const (
	OpenLong           Action = "open_long"
	OpenShort          Action = "open_short"
	OpenLongPending    Action = "open_long_pending"
	OpenShortPending   Action = "open_short_pending"
	CloseLong          Action = "close_long"
	CloseShort         Action = "close_short"
	CloseAll           Action = "close_all"
	CancelPendingLong  Action = "cancel_pending_long"
	CancelPendingShort Action = "cancel_pending_short"
	ModifyStopsLong    Action = "modify_stops_long"
	ModifyStopsShort   Action = "modify_stops_short"
	CloseLongPartial   Action = "close_long_partial"
	CloseShortPartial  Action = "close_short_partial"
	CloseLongVolume    Action = "close_long_volume"
	CloseShortVolume   Action = "close_short_volume"
	AgentPause         Action = "agent_pause"
	AgentResume        Action = "agent_resume"
)

var knownActions = map[Action]bool{
	OpenLong: true, OpenShort: true, OpenLongPending: true, OpenShortPending: true,
	CloseLong: true, CloseShort: true, CloseAll: true,
	CancelPendingLong: true, CancelPendingShort: true,
	ModifyStopsLong: true, ModifyStopsShort: true,
	CloseLongPartial: true, CloseShortPartial: true,
	CloseLongVolume: true, CloseShortVolume: true,
	AgentPause: true, AgentResume: true,
}

// SizingMode selects the server-side volume resolution strategy (spec §3.2, §6).
type SizingMode string

const (
	FixedLots          SizingMode = "fixed_lots"
	CashRisk           SizingMode = "cash_risk"
	PercentBalanceLots SizingMode = "percent_balance_lots"
	PercentBalanceRisk SizingMode = "percent_balance_risk"
)

var knownSizingModes = map[SizingMode]bool{
	FixedLots: true, CashRisk: true, PercentBalanceLots: true, PercentBalanceRisk: true,
}

// StopType is the unit of a stop/target value (spec §3.2).
type StopType string

const (
	StopPips    StopType = "pips"
	StopPrice   StopType = "price"
	StopPercent StopType = "percent"
)

var knownStopTypes = map[StopType]bool{StopPips: true, StopPrice: true, StopPercent: true}

// PartialFraction is the fixed set of allowed partial-close fractions, expressed
// as whole-number percentages (spec §3.2 "a fixed set (e.g. 25/34/50%)").
type PartialFraction int

const (
	Partial25 PartialFraction = 25
	Partial34 PartialFraction = 34
	Partial50 PartialFraction = 50
)

var knownPartialFractions = map[PartialFraction]bool{Partial25: true, Partial34: true, Partial50: true}

// Sizing is the entry volume directive (spec §3.2).
type Sizing struct {
	Mode  SizingMode `json:"mode"`
	Value float64    `json:"value"`
}

// Breakeven configures protective-stop automation (spec §3.2).
type Breakeven struct {
	TriggerPips float64 `json:"trigger_pips"`
	OffsetPips  float64 `json:"offset_pips"`
}

// Trailing configures trailing-stop automation; either the fixed form
// (Trigger/Distance/Step) or the ATR-based variant (spec §3.2).
type Trailing struct {
	Trigger  float64 `json:"trigger,omitempty"`
	Distance float64 `json:"distance,omitempty"`
	Step     float64 `json:"step,omitempty"`

	ATRTimeframe  string  `json:"atr_timeframe,omitempty"`
	ATRTrigger    float64 `json:"atr_trigger,omitempty"`
	ATRPeriod     int     `json:"atr_period,omitempty"`
	ATRMultiplier float64 `json:"atr_multiplier,omitempty"`
	ATRShift      int     `json:"atr_shift,omitempty"`
}

// Instruction is the structured trade instruction (spec §3.2). Pointer fields
// are optional; nil means "not supplied".
type Instruction struct {
	ConnectionKey string  `json:"connection_key"`
	Action        Action  `json:"action"`
	Symbol        string  `json:"symbol,omitempty"`
	Sizing        *Sizing `json:"sizing,omitempty"`

	StopLoss   *float64 `json:"stop_loss,omitempty"`
	StopType   StopType `json:"stop_type,omitempty"`
	TakeProfit *float64 `json:"take_profit,omitempty"`
	TargetType StopType `json:"target_type,omitempty"`

	PendingPrice *float64 `json:"pending_price,omitempty"`
	PriceBasis   string   `json:"price_basis,omitempty"`

	Breakeven *Breakeven `json:"breakeven,omitempty"`
	Trailing  *Trailing  `json:"trailing,omitempty"`

	MaxSpread     *float64 `json:"max_spread,omitempty"`
	AccountFilter *float64 `json:"account_filter,omitempty"`

	PartialFraction *PartialFraction `json:"partial_fraction,omitempty"`
	CloseVolume     *float64         `json:"close_volume,omitempty"`

	Comment       string `json:"comment,omitempty"`
	SchemaVersion int    `json:"schema_version"`

	// IdempotencyKey carries (or receives) the dedup key (FR-4). Optional in the
	// payload; the relay assigns one if absent.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// reservedCommentChars are delimiter characters disallowed in a user comment
// (spec §3.2). Assumption: ';', '|', and newline are reserved; documented in
// BUILD-REPORT.md.
const reservedCommentChars = ";|\n\r"

// Classification helpers ------------------------------------------------------

// IsEntry reports whether the action opens a new position (market or pending).
func (a Action) IsEntry() bool {
	switch a {
	case OpenLong, OpenShort, OpenLongPending, OpenShortPending:
		return true
	}
	return false
}

// IsPending reports whether the action is a pending (not market) entry.
func (a Action) IsPending() bool {
	return a == OpenLongPending || a == OpenShortPending
}

// IsOrderAction reports whether the action requires a symbol (everything except
// the agent control verbs).
func (a Action) IsOrderAction() bool {
	return a != AgentPause && a != AgentResume
}

// IsPartialClose reports whether the action closes a fraction of a position.
func (a Action) IsPartialClose() bool {
	return a == CloseLongPartial || a == CloseShortPartial
}

// IsVolumeClose reports whether the action closes up to a specified volume.
func (a Action) IsVolumeClose() bool {
	return a == CloseLongVolume || a == CloseShortVolume
}

// Known reports whether the action is a recognized verb.
func (a Action) Known() bool { return knownActions[a] }

// ValidationError couples a reason code with a human message (spec §8).
type ValidationError struct {
	Code    reason.Code
	Message string
}

func (e *ValidationError) Error() string { return string(e.Code) + ": " + e.Message }

func verr(c reason.Code, msg string) *ValidationError {
	return &ValidationError{Code: c, Message: msg}
}

// Validate checks the instruction against the schema (FR-6). It returns a
// *ValidationError carrying a stable reason code, or nil if valid. Schema-version
// and action-shape problems map to schema_version_unsupported / malformed_payload
// per spec §8 / TC-X3.
func (in *Instruction) Validate() *ValidationError {
	if in.SchemaVersion != SupportedSchemaVersion {
		return verr(reason.SchemaVersionUnsupported,
			"unsupported schema_version")
	}
	if !in.Action.Known() {
		return verr(reason.MalformedPayload, "unknown action")
	}
	if strings.ContainsAny(in.Comment, reservedCommentChars) {
		return verr(reason.MalformedPayload, "comment contains a reserved delimiter character")
	}

	// Symbol required for order actions.
	if in.Action.IsOrderAction() && strings.TrimSpace(in.Symbol) == "" {
		return verr(reason.MalformedPayload, "symbol is required for this action")
	}

	// Entries require sizing.
	if in.Action.IsEntry() {
		if in.Sizing == nil {
			return verr(reason.MalformedPayload, "sizing is required for entries")
		}
		if !knownSizingModes[in.Sizing.Mode] {
			return verr(reason.MalformedPayload, "unknown sizing mode")
		}
		if in.Sizing.Value <= 0 {
			return verr(reason.MalformedPayload, "sizing value must be positive")
		}
	}

	// Pending entries require a pending price.
	if in.Action.IsPending() && in.PendingPrice == nil {
		return verr(reason.MalformedPayload, "pending_price is required for pending actions")
	}

	// Stop / target units must be valid when values are present.
	if in.StopLoss != nil && in.StopType != "" && !knownStopTypes[in.StopType] {
		return verr(reason.MalformedPayload, "unknown stop_type")
	}
	if in.TakeProfit != nil && in.TargetType != "" && !knownStopTypes[in.TargetType] {
		return verr(reason.MalformedPayload, "unknown target_type")
	}

	// Partial close requires a fraction from the fixed set.
	if in.Action.IsPartialClose() {
		if in.PartialFraction == nil {
			return verr(reason.MalformedPayload, "partial_fraction is required for partial closes")
		}
		if !knownPartialFractions[*in.PartialFraction] {
			return verr(reason.MalformedPayload, "partial_fraction is not in the allowed set")
		}
	}

	// Volume close requires a positive close_volume.
	if in.Action.IsVolumeClose() {
		if in.CloseVolume == nil || *in.CloseVolume <= 0 {
			return verr(reason.MalformedPayload, "close_volume must be positive for volume closes")
		}
	}

	return nil
}
