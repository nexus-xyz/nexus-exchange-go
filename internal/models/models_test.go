package models

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// roundTrip decodes in into a fresh T, re-encodes it, and fails unless the
// output is the same JSON document. Numbers are compared as their literal
// text, so a digit lost to float64 would show.
func roundTrip[T any](t *testing.T, in string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(in), &v); err != nil {
		t.Fatalf("decode %T: %v", v, err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encode %T: %v", v, err)
	}
	if !sameJSON(t, in, string(out)) {
		t.Errorf("%T round trip:\n got %s\nwant %s", v, out, in)
	}
	return v
}

func sameJSON(t *testing.T, a, b string) bool {
	t.Helper()
	v := make([]any, 2)
	for i, s := range []string{a, b} {
		d := json.NewDecoder(strings.NewReader(s))
		d.UseNumber()
		if err := d.Decode(&v[i]); err != nil {
			t.Fatal(err)
		}
	}
	return reflect.DeepEqual(v[0], v[1])
}

// TestCreateOrderRoundTrip covers POST /orders both ways. R2.8: the request is
// snake_case. At the pinned v0.8.1 the response (OrderResponse, wrapping Order)
// is snake_case too; the CCXT reshape that makes it mixed-case (symbol,
// amount, clientOrderId) landed in later spec versions. The mixed-case half of
// R2.8 is covered at v0.8.1 by Trade, below.
func TestCreateOrderRoundTrip(t *testing.T) {
	req := roundTrip[OrderRequest](t, `{"market_id":"BTC-USDX-PERP","side":"Sell","order_type":"StopLimit",`+
		`"trigger_price":"48000","price":"47900.10","quantity":"0.100","time_in_force":"GTC"}`)
	if req.MarketId != "BTC-USDX-PERP" || req.Quantity.String() != "0.100" {
		t.Errorf("decoded %+v", req)
	}

	// Built in Go, the request carries exactly the spec's snake_case keys and
	// nothing for the fields left unset.
	q, _ := ParseDecimal("1")
	out, _ := json.Marshal(OrderRequest{MarketId: "ETH-USDX-PERP", Side: OrderRequestSideBuy,
		OrderType: OrderRequestOrderTypeMarket, Quantity: q, TimeInForce: OrderRequestTimeInForceIOC})
	want := `{"market_id":"ETH-USDX-PERP","side":"Buy","order_type":"Market","quantity":"1","time_in_force":"IOC"}`
	if !sameJSON(t, string(out), want) {
		t.Errorf("encoded %s, want %s", out, want)
	}

	resp := roundTrip[OrderResponse](t, `{"order":{"id":"0b6c2a9e-7f65-4c1e-9f0e-2d7b1f9a3c11",`+
		`"market_id":"BTC-USDX-PERP","account_id":"0xabc","side":"Sell","order_type":"StopLimit",`+
		`"limit_offset_bps":null,"stp":null,"max_slippage_bps":null,"price":"47900.10","quantity":"0.100",`+
		`"filled_qty":"0","status":"Open","cancellation_reason":null,"time_in_force":"GTC",`+
		`"created_at":1758700800123,"updated_at":1758700800123},"fills":[]}`)
	if resp.Order.Price.String() != "47900.10" || *resp.Order.CreatedAt != 1758700800123 {
		t.Errorf("decoded %+v", resp.Order)
	}
}

// TestMixedCaseObjectRoundTrip: one object, CCXT camelCase for fields CCXT
// defines (takerOrMaker) and snake_case for the Nexus extension (is_liquidation).
func TestMixedCaseObjectRoundTrip(t *testing.T) {
	tr := roundTrip[Trade](t, `{"id":"t1","symbol":"BTC-USDX-PERP","timestamp":1758700800123,`+
		`"datetime":"2025-09-24T08:00:00.123Z","side":"buy","price":65000.10,"amount":0.5,"cost":32500.05,`+
		`"takerOrMaker":"taker","is_liquidation":false,"info":{}}`)
	if tr.TakerOrMaker.MustGet() != "taker" || *tr.IsLiquidation || tr.Price.String() != "65000.10" {
		t.Errorf("decoded %+v", tr)
	}
}

// TestUnknownEnumValue: a server-added enum value decodes without error and
// is retrievable, so a pinned client survives an additive change (R3.3).
func TestUnknownEnumValue(t *testing.T) {
	o := roundTrip[Order](t, `{"side":"Buy","status":"Suspended"}`)
	if *o.Status != "Suspended" || o.Status.Valid() || !o.Side.Valid() {
		t.Errorf("status %q valid=%v", *o.Status, o.Status.Valid())
	}
}

// TestNullableVersusAbsent: Ticker.high is declared ["number","null"]. An
// explicit null and a missing key decode differently and re-encode as served.
func TestNullableVersusAbsent(t *testing.T) {
	tk := roundTrip[Ticker](t, `{"symbol":"BTC-USDX-PERP","high":null,"bid":65000.10}`)
	if !tk.High.IsSpecified() || !tk.High.IsNull() {
		t.Error("high: want present and null")
	}
	if tk.Low.IsSpecified() {
		t.Error("low: want absent")
	}
	if tk.Bid.MustGet() != "65000.10" {
		t.Errorf("bid = %s", tk.Bid.MustGet())
	}

	// The same holds for a nullable money field on a request.
	r := roundTrip[OrderRequest](t, `{"market_id":"m","side":"Buy","order_type":"Limit","price":"1",`+
		`"quantity":"1","time_in_force":"GTC","trigger_price":null}`)
	if !r.TriggerPrice.IsNull() || r.StopPrice.IsSpecified() {
		t.Error("trigger_price: want null; stop_price: want absent")
	}
}

// TestBatchResultUnion: POST /orders/batch entries are a oneOf discriminated
// by outcome.
func TestBatchResultUnion(t *testing.T) {
	var rs []OrderResult
	in := `[{"outcome":"ok","order":{"status":"Open"},"fills":[]},{"outcome":"err","error":"insufficient_margin","message":"no"}]`
	if err := json.Unmarshal([]byte(in), &rs); err != nil {
		t.Fatal(err)
	}
	ok, err := rs[0].ValueByDiscriminator()
	if o, isOk := ok.(OrderResultOk); err != nil || !isOk || *o.Order.Status != OrderStatusOpen {
		t.Errorf("rs[0] = %#v, %v", ok, err)
	}
	bad, err := rs[1].ValueByDiscriminator()
	if e, isErr := bad.(OrderResultErr); err != nil || !isErr || e.Error != "insufficient_margin" {
		t.Errorf("rs[1] = %#v, %v", bad, err)
	}
}

// servedMarket is one /markets row verbatim as public testnet served it on
// 2026-10-05 (SERVED_MARKET in nexus-exchange-py): CCXT's id/base/quote, not
// the pinned spec's market_id/base_asset/quote_asset (ENG-19679).
const servedMarket = `{"active":true,"base":"BTC","contractSize":"1","funding_rate_cap":"0.001",` +
	`"id":"BTC-USDX-PERP","initial_margin_rate":"0.02","lifecycle":"active","lot_size":"0.001",` +
	`"maintenance_margin_rate":"0.01","maker_rebate_bps":-2,"marginModes":{"cross":true,"isolated":true},` +
	`"max_leverage":50,"max_open_interest":"10000","max_open_interest_notional":null,"max_order_size":"100",` +
	`"min_order_size":"0.001","price_band_bps":500,"quote":"USDX","settle":"USDX","taker_fee_bps":5,` +
	`"tick_size":"0.5","type":"swap"}`

// TestMarketServedShape: the served names decode, the spec's names are the
// fallback, and a row with neither id is an error rather than a nil MarketId.
func TestMarketServedShape(t *testing.T) {
	var ms []Market
	if err := json.Unmarshal([]byte("["+servedMarket+"]"), &ms); err != nil {
		t.Fatal(err)
	}
	m := ms[0]
	if m.MarketId == nil || *m.MarketId != "BTC-USDX-PERP" || m.BaseAsset == nil || *m.BaseAsset != "BTC" ||
		m.QuoteAsset == nil || *m.QuoteAsset != "USDX" || m.TickSize.String() != "0.5" ||
		m.MinOrderSize.String() != "0.001" || *m.MaxLeverage != 50 {
		t.Errorf("decoded %+v", m)
	}

	roundTrip[Market](t, `{"market_id":"ETH-USDX-PERP","base_asset":"ETH","quote_asset":"USDX","tick_size":"0.10"}`)

	if err := json.Unmarshal([]byte(`{"base":"BTC","tick_size":"0.5"}`), &m); err == nil {
		t.Error("a row without id or market_id decoded")
	}
}

// TestAccountFeesFractionalRates: AccountFees is hand-written (account_fees.go)
// so the rates decode to 0.1 bps while the pinned spec still types them
// integer (ENG-21111). A whole rate stays an integer and a fractional one keeps
// its digits, both ways.
func TestAccountFeesFractionalRates(t *testing.T) {
	for _, in := range []string{
		`{"discounts":[],"maker_fee_bps":-2,"schedule":"standard","taker_fee_bps":5,"tier":"base","volume_30d":"1","volume_30d_estimated":false}`,
		`{"discounts":[],"maker_fee_bps":-0.4,"schedule":"standard","taker_fee_bps":2.8,"tier":"base","volume_30d":"1","volume_30d_estimated":false}`,
		`{"discounts":[],"maker_fee_bps":0.4,"schedule":"standard","taker_fee_bps":2.8,"tier":"base","volume_30d":"1","volume_30d_estimated":false}`,
	} {
		roundTrip[AccountFees](t, in)
	}
	f := roundTrip[AccountFees](t, `{"discounts":[],"maker_fee_bps":-0.4,"schedule":"standard","taker_fee_bps":2.8,"tier":"base","volume_30d":"1","volume_30d_estimated":false}`)
	if f.MakerFeeBps != "-0.4" || f.TakerFeeBps != "2.8" {
		t.Errorf("fees = %q / %q", f.MakerFeeBps, f.TakerFeeBps)
	}
}

// TestMarketFractionalFeeRates: Market does not declare taker_fee_bps or
// maker_rebate_bps (the pinned spec does not), so a row carrying a rate to 0.1
// bps must still decode (ENG-21111).
func TestMarketFractionalFeeRates(t *testing.T) {
	row := strings.NewReplacer(`"maker_rebate_bps":-2`, `"maker_rebate_bps":-0.4`,
		`"taker_fee_bps":5`, `"taker_fee_bps":2.8`).Replace(servedMarket)
	if row == servedMarket {
		t.Fatal("fixture did not change")
	}
	var m Market
	if err := json.Unmarshal([]byte(row), &m); err != nil {
		t.Fatal(err)
	}
	if m.MarketId == nil || *m.MarketId != "BTC-USDX-PERP" {
		t.Errorf("decoded %+v", m)
	}
}
