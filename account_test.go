package nexus

import (
	"context"
	"errors"
	"testing"
)

// TestMarginUnavailableFailsClosed: a 502 authoritative_margin_unavailable on
// balance or positions is ErrMarginUnavailable with no value, never a
// substitute.
func TestMarginUnavailableFailsClosed(t *testing.T) {
	ctx := context.Background()
	body := `{"code":"authoritative_margin_unavailable","message":"engine margin view unavailable"}`
	t.Run("balance", func(t *testing.T) {
		c, _ := recorder(t, 502, body)
		a, err := c.Account().FetchBalance(ctx)
		if !errors.Is(err, ErrMarginUnavailable) || a != nil {
			t.Fatalf("FetchBalance = %v, %v; want nil, ErrMarginUnavailable", a, err)
		}
	})
	t.Run("positions", func(t *testing.T) {
		c, _ := recorder(t, 502, body)
		p, err := c.FetchPositions(ctx)
		if !errors.Is(err, ErrMarginUnavailable) || p != nil {
			t.Fatalf("FetchPositions = %v, %v; want nil, ErrMarginUnavailable", p, err)
		}
	})
}

// TestFeeRatesDecodeToATenthOfABasisPoint: the server sends fee rates to 0.1
// bps (2.8, -0.4) on GET /account/fees and GET /markets, and a whole rate as an
// integer (ENG-21111). Each decodes, and the account rates keep the served
// digits exactly. A positive maker rate is a fee the maker pays.
func TestFeeRatesDecodeToATenthOfABasisPoint(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct{ maker, taker string }{
		{"-2", "5"},
		{"-0.4", "2.8"},
		{"0.4", "2.8"},
	} {
		t.Run(tc.maker+"/"+tc.taker, func(t *testing.T) {
			c, _ := recorder(t, 200, `{"maker_fee_bps":`+tc.maker+`,"taker_fee_bps":`+tc.taker+
				`,"tier":"base","schedule":"standard","markets":[],"volume_30d":"1",`+
				`"volume_30d_estimated":false,"discounts":[]}`)
			f, err := c.Account().FetchTradingFees(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if f.MakerFeeBps.String() != tc.maker || f.TakerFeeBps.String() != tc.taker {
				t.Errorf("fees = %s / %s, want %s / %s", f.MakerFeeBps, f.TakerFeeBps, tc.maker, tc.taker)
			}
			// The served digits are a valid Decimal, so exact arithmetic is one call away.
			if d, err := ParseDecimal(f.TakerFeeBps.String()); err != nil || d.String() != tc.taker {
				t.Errorf("ParseDecimal(%s) = %v, %v", f.TakerFeeBps, d, err)
			}

			c, _ = recorder(t, 200, `[{"id":"BTC-USDX-PERP","base":"BTC","quote":"USDX","tick_size":"0.5",`+
				`"taker_fee_bps":`+tc.taker+`,"maker_rebate_bps":`+tc.maker+`}]`)
			ms, err := c.FetchMarkets(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(ms) != 1 || ms[0].MarketId == nil || *ms[0].MarketId != "BTC-USDX-PERP" {
				t.Errorf("markets = %+v", ms)
			}
		})
	}
}
