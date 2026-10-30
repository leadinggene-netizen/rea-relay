// Package reason defines the stable machine-readable reason codes used across
// the relay (spec §8). Every rejection or skip carries one of these codes plus
// a human-readable message, and is recorded to the audit log.
package reason

// Code is a stable machine reason code. The exact string values are part of the
// public contract (spec §8) and must not change.
type Code string

// Reason codes enumerated by spec §8. The comment notes the originating
// requirement / edge case / test case.
const (
	// Authentication & ingestion (§3.1, §8).
	MissingKey               Code = "missing_key"                // EC-1, TC-X1: blank/placeholder connection key.
	UnknownKey               Code = "unknown_key"                // FR-2: key not found.
	ExpiredKey               Code = "expired_key"                // FR-2, FR-20, EC-15: key past UTC expiry boundary.
	BadSignature             Code = "bad_signature"              // FR-3, TC-X2: signature required but missing/invalid (incl. replay).
	MalformedPayload         Code = "malformed_payload"          // TC-X3: unparseable payload or invalid/unknown field value.
	SchemaVersionUnsupported Code = "schema_version_unsupported" // TC-X3: schema_version not supported.

	// Processing & resolution (§3.3, §6, §8).
	SymbolUnmapped   Code = "symbol_unmapped"     // FR-6, EC-2, TC-E1: symbol has no mapping.
	RiskRequiresStop Code = "risk_requires_stop"  // §6, EC-3, TC-E2: sizing mode needs a stop but none given.
	VolumeOutOfRange Code = "volume_out_of_range" // §6, EC-4, TC-E3: resolved volume below broker min / above max.
	VolumeRounded    Code = "volume_rounded"      // FR-14, EC-5, TC-E4: lot-step rounding changed the final volume (advisory).

	// Pre-trade gates & execution feedback (§3.4, §8).
	SpreadExceeded       Code = "spread_exceeded"        // FR-15, EC-6, TC-E6: current spread above max_spread.
	AccountFilterBlocked Code = "account_filter_blocked" // FR-15, EC-7: account_filter threshold not met.
	NoMatchingPosition   Code = "no_matching_position"   // FR-13, EC-10: modify/close with no matching open position (no-op).
	BrokerRejected       Code = "broker_rejected"        // FR-16, TC-X5: broker rejected the order (broker code surfaced).

	// Delivery & dedup (§3.3, §8).
	DuplicateSuppressed Code = "duplicate_suppressed" // FR-4, EC-8, TC-E5: repeat idempotency key within window.
	DeliveryExpired     Code = "delivery_expired"     // FR-9, EC-9, TC-E7: buffered delivery aged out of the replay window.

	// Connections & licensing (§3.5, §8).
	ConnectionLimit Code = "connection_limit" // FR-17, EC-14, TC-X4: over the tier's concurrent-connection limit.
	AgentPaused     Code = "agent_paused"     // FR-11, TC-N6: connection paused, new entries halted.

	// RateLimited is used when a per-connection inbound rate limit sheds an
	// instruction (FR-5, EC-13). It is NOT in the minimum §8 list; see
	// BUILD-REPORT.md "assumptions". The relay prefers queueing over shedding so
	// already-accepted instructions are never lost.
	RateLimited Code = "rate_limited"
)

// messages maps codes to default human-readable text.
var messages = map[Code]string{
	MissingKey:               "connection key is missing or a placeholder",
	UnknownKey:               "connection key is not recognized",
	ExpiredKey:               "connection key has expired",
	BadSignature:             "instruction signature is missing or invalid",
	MalformedPayload:         "instruction payload is malformed or contains an invalid value",
	SchemaVersionUnsupported: "instruction schema_version is not supported",
	SymbolUnmapped:           "symbol has no mapping in this connection's symbol table",
	RiskRequiresStop:         "this sizing mode requires a stop loss but none was supplied",
	VolumeOutOfRange:         "resolved volume is outside the broker's min/max range",
	VolumeRounded:            "resolved volume was rounded to the broker lot step",
	SpreadExceeded:           "current spread exceeds max_spread",
	AccountFilterBlocked:     "account_filter threshold not met",
	NoMatchingPosition:       "no matching open position for the requested action",
	BrokerRejected:           "broker rejected the order",
	DuplicateSuppressed:      "duplicate idempotency key within window; suppressed",
	DeliveryExpired:          "buffered delivery expired before the agent reconnected",
	ConnectionLimit:          "concurrent connection limit for the subscription tier reached",
	AgentPaused:              "connection is paused; new entries are halted",
	RateLimited:              "per-connection inbound rate limit exceeded",
}

// Message returns the default human-readable message for a code.
func (c Code) Message() string {
	if m, ok := messages[c]; ok {
		return m
	}
	return string(c)
}

// String implements fmt.Stringer.
func (c Code) String() string { return string(c) }
