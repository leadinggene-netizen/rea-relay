package risk

import (
	"math"
	"testing"

	"github.com/leadinggene-netizen/rea-relay/internal/connection"
	"github.com/leadinggene-netizen/rea-relay/internal/instruction"
	"github.com/leadinggene-netizen/rea-relay/internal/reason"
)

var spec = connection.BrokerSymbolSpec{
	BrokerSymbol: "XAUUSD.m", PipSize: 0.1, PipValuePerLot: 1.0,
	LotMin: 0.01, LotMax: 100, LotStep: 0.01,
}

var quote = Quote{Bid: 2400.0, Ask: 2400.2, Spread: 0.2}

func pf(v float64) *float64 { return &v }

func TestCashRiskMath(t *testing.T) {
	in := &instruction.Instruction{
		Action: instruction.OpenShort, Symbol: "GOLD",
		Sizing:   &instruction.Sizing{Mode: instruction.CashRisk, Value: 100},
		StopLoss: pf(25), StopType: instruction.StopPips,
	}
	res, err := Resolve(in, spec, quote, 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// L = 100 / (25 * 1) = 4.0.
	if res.Lots != 4.0 {
		t.Fatalf("want 4.0 lots, got %v", res.Lots)
	}
}

func TestPercentBalanceRiskMath(t *testing.T) {
	in := &instruction.Instruction{
		Action: instruction.OpenLong, Symbol: "GOLD",
		Sizing:   &instruction.Sizing{Mode: instruction.PercentBalanceRisk, Value: 1}, // 1% of 10000 = 100 risk
		StopLoss: pf(25), StopType: instruction.StopPips,
	}
	res, err := Resolve(in, spec, quote, 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// risk = 100; L = 100 / (25*1) = 4.0.
	if res.Lots != 4.0 {
		t.Fatalf("want 4.0 lots, got %v", res.Lots)
	}
}

func TestPercentBalanceLotsMath(t *testing.T) {
	in := &instruction.Instruction{
		Action: instruction.OpenLong, Symbol: "GOLD",
		Sizing: &instruction.Sizing{Mode: instruction.PercentBalanceLots, Value: 1}, // 1% of 10000
	}
	res, err := Resolve(in, spec, quote, 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// lots = 10000 * 0.01 * 0.0001 = 0.01.
	if res.Lots != 0.01 {
		t.Fatalf("want 0.01 lots, got %v", res.Lots)
	}
}

func TestRiskRequiresStop(t *testing.T) {
	in := &instruction.Instruction{
		Action: instruction.OpenLong, Symbol: "GOLD",
		Sizing: &instruction.Sizing{Mode: instruction.CashRisk, Value: 100},
	}
	_, err := Resolve(in, spec, quote, 10000)
	if err == nil || err.Code != reason.RiskRequiresStop {
		t.Fatalf("want risk_requires_stop, got %v", err)
	}
}

func TestLotStepRoundingFlag(t *testing.T) {
	in := &instruction.Instruction{
		Action: instruction.OpenLong, Symbol: "GOLD",
		Sizing: &instruction.Sizing{Mode: instruction.FixedLots, Value: 0.117},
	}
	res, err := Resolve(in, spec, quote, 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Lots != 0.12 || !res.Rounded {
		t.Fatalf("want 0.12 rounded, got lots=%v rounded=%v", res.Lots, res.Rounded)
	}
}

func TestVolumeOutOfRange(t *testing.T) {
	in := &instruction.Instruction{
		Action: instruction.OpenLong, Symbol: "GOLD",
		Sizing: &instruction.Sizing{Mode: instruction.FixedLots, Value: 0.004},
	}
	_, err := Resolve(in, spec, quote, 10000)
	if err == nil || err.Code != reason.VolumeOutOfRange {
		t.Fatalf("want volume_out_of_range, got %v", err)
	}
}

func TestSLTPPriceDirections(t *testing.T) {
	// Long: SL below entry, TP above.
	long := &instruction.Instruction{
		Action: instruction.OpenLong, Symbol: "GOLD",
		Sizing:   &instruction.Sizing{Mode: instruction.FixedLots, Value: 0.1},
		StopLoss: pf(20), StopType: instruction.StopPips,
		TakeProfit: pf(40), TargetType: instruction.StopPips,
	}
	res, _ := Resolve(long, spec, quote, 10000)
	if math.Abs(*res.SLPrice-(2400.2-2.0)) > 1e-9 || math.Abs(*res.TPPrice-(2400.2+4.0)) > 1e-9 {
		t.Fatalf("long SL/TP wrong: SL=%v TP=%v", *res.SLPrice, *res.TPPrice)
	}
	// Short: SL above entry (bid), TP below.
	short := &instruction.Instruction{
		Action: instruction.OpenShort, Symbol: "GOLD",
		Sizing:   &instruction.Sizing{Mode: instruction.FixedLots, Value: 0.1},
		StopLoss: pf(20), StopType: instruction.StopPips,
		TakeProfit: pf(40), TargetType: instruction.StopPips,
	}
	res2, _ := Resolve(short, spec, quote, 10000)
	if math.Abs(*res2.SLPrice-(2400.0+2.0)) > 1e-9 || math.Abs(*res2.TPPrice-(2400.0-4.0)) > 1e-9 {
		t.Fatalf("short SL/TP wrong: SL=%v TP=%v", *res2.SLPrice, *res2.TPPrice)
	}
}
