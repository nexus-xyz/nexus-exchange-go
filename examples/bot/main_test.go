package main

import (
	"math/big"
	"testing"

	nexus "github.com/nexus-xyz/nexus-exchange-go"
)

func TestQuote(t *testing.T) {
	dec := func(s string) nexus.Decimal {
		d, err := nexus.ParseDecimal(s)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	tick := dec("0.50")
	q := quoter{offset: new(big.Rat).Mul(tick.Rat(), big.NewRat(3, 1)), decimals: decimals(tick.String())}

	for _, tc := range []struct {
		side       nexus.OrderSide
		best, want string
	}{
		{nexus.Buy, "100.5", "99.00"},
		{nexus.Sell, "101", "102.50"},
	} {
		r, ok := q.quote(tc.side, dec(tc.best))
		if !ok || r.Price.String() != tc.want || r.TimeInForce != nexus.PostOnly {
			t.Errorf("quote(%s, %s) = %s, %v; want post-only @ %s", tc.side, tc.best, r.Price, ok, tc.want)
		}
	}
	if _, ok := q.quote(nexus.Buy, dec("1.5")); ok {
		t.Error("a bid at or below zero was quoted")
	}
}
