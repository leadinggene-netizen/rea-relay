package transport

import (
	"github.com/leadinggene-netizen/rea-relay/internal/delivery"
	"github.com/leadinggene-netizen/rea-relay/internal/instruction"
	"github.com/leadinggene-netizen/rea-relay/internal/reason"
)

// Message types carried by the typed JSON envelope (TR-3). The same set is used
// on both the WebSocket and the long-poll transports.
const (
	MsgHello     = "hello"     // agent→relay: negotiate/auth (long-poll carries auth in headers).
	MsgWelcome   = "welcome"   // relay→agent: session established (session_id + resume_token).
	MsgOrder     = "order"     // relay→agent: a fully-resolved order with delivery_seq.
	MsgAck       = "ack"       // agent→relay: order received, by delivery_seq.
	MsgResult    = "result"    // agent→relay: execution outcome (filled/partial/rejected + broker code).
	MsgHeartbeat = "heartbeat" // agent→relay: keepalive.
	MsgPong      = "pong"      // relay→agent: heartbeat acknowledgement.
	MsgControl   = "control"   // relay→agent: pause/resume.
	MsgError     = "error"     // either direction: a stable reason code + message.
)

// WireOrder is the on-the-wire representation of a fully-resolved order (FR-7).
// The agent performs no risk math: it fills the already-resolved volume and
// absolute SL/TP. Nil pointers mean "not supplied".
type WireOrder struct {
	InstructionID   string                       `json:"instruction_id"`
	IdempotencyKey  string                       `json:"idempotency_key"`
	AccountID       string                       `json:"account_id"`
	Action          instruction.Action           `json:"action"`
	BrokerSymbol    string                       `json:"broker_symbol"`
	Lots            float64                      `json:"lots,omitempty"`
	SLPrice         *float64                     `json:"sl_price,omitempty"`
	TPPrice         *float64                     `json:"tp_price,omitempty"`
	PendingPrice    *float64                     `json:"pending_price,omitempty"`
	PartialFraction *instruction.PartialFraction `json:"partial_fraction,omitempty"`
	CloseVolume     *float64                     `json:"close_volume,omitempty"`
	Comment         string                       `json:"comment,omitempty"`
}

// WireResult is the agent's reported execution outcome (FR-16, TR-3).
type WireResult struct {
	AccountID   string          `json:"account_id"`
	Status      delivery.Status `json:"status"` // filled|partial|rejected|skipped|no_op
	FinalVolume float64         `json:"final_volume,omitempty"`
	BrokerCode  string          `json:"broker_code,omitempty"`
	Reason      reason.Code     `json:"reason,omitempty"`
}

// WireError carries a stable §8 reason code plus a human message (TR-3).
type WireError struct {
	Code    reason.Code `json:"code"`
	Message string      `json:"message"`
}

// Envelope is the single typed JSON message shape used across the transport
// (TR-3). Exactly one of the payload fields is set, selected by Type.
type Envelope struct {
	Type        string `json:"type"`
	DeliverySeq int64  `json:"delivery_seq,omitempty"`

	// Handshake / session fields.
	SessionID     string `json:"session_id,omitempty"`
	ConnectionKey string `json:"connection_key,omitempty"`
	ResumeToken   int64  `json:"resume_token,omitempty"`

	// HMAC handshake material (TR-2), when the connection requires signatures.
	Timestamp int64  `json:"ts,omitempty"`
	Nonce     string `json:"nonce,omitempty"`
	Signature string `json:"sig,omitempty"`

	// Payloads (one per Type).
	Order   *WireOrder  `json:"order,omitempty"`
	Result  *WireResult `json:"result,omitempty"`
	Control string      `json:"control,omitempty"` // "pause" | "resume"
	Error   *WireError  `json:"error,omitempty"`
}

// orderEnvelope builds an `order` message from a resolved order + delivery seq.
func orderEnvelope(seq int64, accountID string, o delivery.ResolvedOrder) Envelope {
	return Envelope{
		Type:        MsgOrder,
		DeliverySeq: seq,
		Order: &WireOrder{
			InstructionID:   o.InstructionID,
			IdempotencyKey:  o.IdempotencyKey,
			AccountID:       accountID,
			Action:          o.Action,
			BrokerSymbol:    o.BrokerSymbol,
			Lots:            o.Lots,
			SLPrice:         o.SLPrice,
			TPPrice:         o.TPPrice,
			PendingPrice:    o.PendingPrice,
			PartialFraction: o.PartialFraction,
			CloseVolume:     o.CloseVolume,
			Comment:         o.Comment,
		},
	}
}

// accountResult converts a wire result into the internal delivery result.
func (wr *WireResult) accountResult() delivery.AccountResult {
	return delivery.AccountResult{
		AccountID:   wr.AccountID,
		Status:      wr.Status,
		FinalVolume: wr.FinalVolume,
		BrokerCode:  wr.BrokerCode,
		Reason:      wr.Reason,
	}
}
