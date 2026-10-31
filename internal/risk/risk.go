// Package risk implements server-side volume resolution and absolute SL/TP
// price computation (FR-7, §6). The terminal agent receives a fully-resolved
// order and performs no risk math.
package risk

import (
	"math"

	"github.com/leadinggene-netizen/rea-relay/internal/connection"
	"github.com/leadinggene-netizen/rea-relay/internal/instruction"
	"github.com/leadinggene-netizen/rea-relay/internal/reason"
)

// Quote is the current market price for a symbol (bid/ask). Spread is in price
// units; it also backs the max_spread gate (FR-15).
type Quote struct {
	Bid    float64
	Ask    float64
	Spread float64 // typically Ask-Bid, carried explicitly
}

// QuoteSource supplies current quotes for a connection's broker symbol.
type QuoteSource interface {
	Quote(brokerSymbol string) (Quote, bool)
}

// PercentBalanceLotFactor controls the percent_balance_lots convention (§6:
// "expressed directly as lots per the configured convention"). Assumption:
// lots = balance * (value/100) * PercentBalanceLotFactor. The default factor
// 0.0001 maps a 1% directive on a 10,000-unit balance to 0.01 lots. Documented
// in BUILD-REPORT.md.
const PercentBalanceLotFactor = 0.0001

// Resolution is the outcome of server-side risk math.
type Resolution struct {
	Lots       float64  // final volume after clamp + lot-step rounding
	TargetLots float64  // pre-rounding target (for volume_rounded detection)
	Rounded    bool     // final volume differs from target due to lot step (EC-5)
	SLPrice    *float64 // absolute stop-loss price (nil if none)
	TPPrice    *float64 // absolute take-profit price (nil if none)
	EntryPrice float64  // the entry price used for resolution
}

// Error couples a reason code with a message (spec §8).
type Error struct {
	Code    reason.Code
	Message string
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

func rerr(c reason.Code) *Error { return &Error{Code: c, Message: c.Message()} }

// Resolve computes absolute volume and SL/TP prices for an entry instruction
// (FR-7, §6). balance is the target account's balance/equity; spec is the
// broker symbol spec; q is the current quote.
//
// Steps: pick entry price; derive stop distance D (pips) when a stop exists;
// size per sizing.mode; reject volume_out_of_range if the target falls outside
// [min,max]; round to the lot step (flagging volume_rounded on a change);
// finally convert SL/TP to absolute prices.
func Resolve(in *instruction.Instruction, spec connection.BrokerSymbolSpec, q Quote, balance float64) (Resolution, *Error) {
	var res Resolution

	isLong := in.Action == instruction.OpenLong || in.Action == instruction.OpenLongPending || in.Action == instruction.FlipLong
	// Entry price: pending uses pending_price; market long uses ask, short bid.
	entry := q.Ask
	if !isLong {
		entry = q.Bid
	}
	if in.Action.IsPending() && in.PendingPrice != nil {
		entry = *in.PendingPrice
	}
	res.EntryPrice = entry

	// Stop distance D in pips (only meaningful when a stop is present).
	hasStop := in.StopLoss != nil
	var dPips float64
	if hasStop {
		dPips = stopDistancePips(*in.StopLoss, in.StopType, entry, spec.PipSize)
	}

	// Size per mode (§6).
	var target float64
	switch in.Sizing.Mode {
	case instruction.FixedLots:
		target = in.Sizing.Value
	case instruction.CashRisk:
		if !hasStop || dPips <= 0 {
			return res, rerr(reason.RiskRequiresStop)
		}
		target = in.Sizing.Value / (dPips * spec.PipValuePerLot)
	case instruction.PercentBalanceLots:
		target = balance * (in.Sizing.Value / 100.0) * PercentBalanceLotFactor
	case instruction.PercentBalanceRisk:
		if !hasStop || dPips <= 0 {
			return res, rerr(reason.RiskRequiresStop)
		}
		riskCash := balance * (in.Sizing.Value / 100.0)
		target = riskCash / (dPips * spec.PipValuePerLot)
	default:
		return res, &Error{Code: reason.MalformedPayload, Message: "unknown sizing mode"}
	}
	res.TargetLots = target

	// Clamp check: a target outside the broker range is a rejection, not a
	// silent clamp (§6 "if clamp rejects", EC-4, TC-E3).
	if target < spec.LotMin || target > spec.LotMax {
		return res, rerr(reason.VolumeOutOfRange)
	}

	// Round to lot step (FR-14, EC-5). Round-half-to-even on the step grid.
	final := roundToStep(target, spec.LotStep)
	// A rounded value could dip below min (e.g. rounds down past min): reject.
	if final < spec.LotMin || final > spec.LotMax {
		return res, rerr(reason.VolumeOutOfRange)
	}
	res.Lots = final
	res.Rounded = math.Abs(final-target) > 1e-12

	// Absolute SL/TP prices.
	if hasStop {
		sl := stopPrice(*in.StopLoss, in.StopType, entry, spec.PipSize, isLong, true)
		res.SLPrice = &sl
	}
	if in.TakeProfit != nil {
		tp := stopPrice(*in.TakeProfit, in.TargetType, entry, spec.PipSize, isLong, false)
		res.TPPrice = &tp
	}
	return res, nil
}

// stopDistancePips converts a stop value in its unit to a pip distance.
func stopDistancePips(val float64, t instruction.StopType, entry, pipSize float64) float64 {
	switch t {
	case instruction.StopPips, "":
		return math.Abs(val)
	case instruction.StopPrice:
		if pipSize == 0 {
			return 0
		}
		return math.Abs(entry-val) / pipSize
	case instruction.StopPercent:
		if pipSize == 0 {
			return 0
		}
		return (entry * val / 100.0) / pipSize
	}
	return 0
}

// stopPrice converts a stop/target value to an absolute price. isStop selects
// the protective direction: a stop sits adverse to the entry, a target
// favorable. For a long, stop is below and target above; reversed for a short.
func stopPrice(val float64, t instruction.StopType, entry, pipSize float64, isLong, isStop bool) float64 {
	// Price type: value is already absolute.
	if t == instruction.StopPrice {
		return val
	}
	var dist float64
	switch t {
	case instruction.StopPips, "":
		dist = math.Abs(val) * pipSize
	case instruction.StopPercent:
		dist = entry * val / 100.0
	}
	// Determine sign: stop is adverse, target favorable.
	below := isStop // for long: stop below (true), target above (false)
	if !isLong {
		below = !below // for short: stop above, target below
	}
	if below {
		return entry - dist
	}
	return entry + dist
}

// roundToStep rounds v to the nearest multiple of step (round-half-up). A
// zero/negative step returns v unchanged.
func roundToStep(v, step float64) float64 {
	if step <= 0 {
		return v
	}
	n := math.Floor(v/step + 0.5)
	r := n * step
	// Normalize floating-point drift to the step's decimal resolution.
	return roundTo(r, decimalsOf(step))
}

func roundTo(v float64, decimals int) float64 {
	p := math.Pow(10, float64(decimals))
	return math.Round(v*p) / p
}

// decimalsOf estimates the number of decimal places implied by a step like 0.01.
func decimalsOf(step float64) int {
	if step <= 0 {
		return 8
	}
	d := 0
	for step < 1 && d < 8 {
		step *= 10
		d++
	}
	return d
}
