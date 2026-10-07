package nexus

import (
	"fmt"
	"math/big"
	"strings"
)

// RoundPrice snaps price onto m's tick_size grid, on the side that never
// rounds toward crossing the book: a Buy rounds toward zero and a Sell away
// from zero (ENG-18543). The exchange rejects an off-tick price, so call it on
// any price you computed rather than read off the book.
//
// It mirrors round_price in the Rust SDK's src/markets.rs, with the side
// choosing Rounding::Down (Buy) or Rounding::Up (Sell). A market with no tick
// (absent or zero) returns price unchanged. The arithmetic is exact, never
// float64, and the result carries no trailing zeros ("50001.0" is "50001").
// An unknown side is an error.
func RoundPrice(m Market, price Decimal, side OrderSide) (Decimal, error) {
	switch side {
	case Buy:
		return roundToIncrement(price, m.TickSize, false), nil
	case Sell:
		return roundToIncrement(price, m.TickSize, true), nil
	}
	return Decimal{}, fmt.Errorf("nexus: RoundPrice: side must be %q or %q, got %q", Buy, Sell, side)
}

// RoundSize snaps size onto m's lot_size grid toward zero, so it never rounds
// up into more risk than asked. It mirrors round_size in the Rust SDK's
// src/markets.rs with its default, Rounding::Down. A market with no lot
// (absent or zero) returns size unchanged.
func RoundSize(m Market, size Decimal) Decimal {
	return roundToIncrement(size, m.LotSize, false)
}

// roundToIncrement is markets.rs's round_to_increment for Down
// (awayFromZero false) and Up (true). Both are sign-symmetric: toward and away
// from zero, not floor and ceil.
func roundToIncrement(v Decimal, inc *Decimal, awayFromZero bool) Decimal {
	if inc == nil {
		return v
	}
	step := inc.Rat()
	if step.Sign() == 0 {
		return v
	}
	steps := new(big.Rat).Quo(v.Rat(), step)
	n, rem := new(big.Int).QuoRem(steps.Num(), steps.Denom(), new(big.Int)) // truncates toward zero
	if awayFromZero && rem.Sign() != 0 {
		n.Add(n, big.NewInt(int64(steps.Sign())))
	}
	// n steps of inc have no more decimals than inc, so this is exact.
	s := new(big.Rat).Mul(new(big.Rat).SetInt(n), step).FloatString(decimalPlaces(inc.String()))
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	d, _ := ParseDecimal(s) // a FloatString with trailing zeros cut is a valid literal
	return d
}

func decimalPlaces(s string) int {
	if i := strings.IndexByte(s, '.'); i >= 0 {
		return len(s) - i - 1
	}
	return 0
}
