package nexus

import "testing"

// roundingCases mirrors the round_* fixtures in nexus-exchange-rs
// tests/markets.rs (tick 0.5, lot 0.001), so a drift between the SDKs shows up
// as a diff against that file. The ts and py SDKs carry the same table.
//
// Not carried over: rs's Rounding::Nearest cases (no side maps to it, see
// RoundPrice) and its Decimal::MAX overflow case (Decimal here has no fixed
// width to overflow). The "extra" rows are not in rs.
var roundingCases = []struct {
	value, inc   string
	awayFromZero bool // rs Rounding::Up; false is Rounding::Down
	want         string
}{
	// round_price_snaps_to_tick
	{"50000.30", "0.5", false, "50000"},
	{"50000.30", "0.5", true, "50000.5"},
	{"50000.5", "0.5", false, "50000.5"},
	// round_size_snaps_to_lot
	{"1.23456", "0.001", false, "1.234"},
	{"1.23456", "0.001", true, "1.235"},
	// round_is_sign_symmetric_for_negatives
	{"-50000.3", "0.5", false, "-50000"},
	{"-50000.3", "0.5", true, "-50000.5"},
	{"-1.23456", "0.001", false, "-1.234"},
	{"-1.23456", "0.001", true, "-1.235"},
	// zero_increment_passes_through
	{"50000.3", "0", false, "50000.3"},
	{"1.23456", "0", true, "1.23456"},
	// round_result_is_clean_scale
	{"50001.0", "0.5", false, "50001"},
	// extra: ticks that float division gets wrong (ENG-19697), and -0
	{"2345.1", "0.1", false, "2345.1"},
	{"0.3", "0.1", true, "0.3"},
	{"123.456", "0.01", false, "123.45"},
	{"123.456", "0.01", true, "123.46"},
	{"7", "0.25", true, "7"},
	{"-0.0004", "0.001", false, "0"},
}

func TestRoundToIncrementMatchesRustFixtures(t *testing.T) {
	for _, c := range roundingCases {
		inc := mustDecimal(t, c.inc)
		got := roundToIncrement(mustDecimal(t, c.value), &inc, c.awayFromZero)
		if got.String() != c.want {
			t.Errorf("round(%s, %s, up=%v) = %s, want %s", c.value, c.inc, c.awayFromZero, got, c.want)
		}
	}
}

func TestRoundPriceAndSize(t *testing.T) {
	tick, lot := mustDecimal(t, "0.5"), mustDecimal(t, "0.001")
	m := Market{TickSize: &tick, LotSize: &lot}
	price := mustDecimal(t, "50000.30")

	if got, err := RoundPrice(m, price, Buy); err != nil || got.String() != "50000" {
		t.Errorf("buy = %s, %v; want 50000 (down)", got, err)
	}
	if got, err := RoundPrice(m, price, Sell); err != nil || got.String() != "50000.5" {
		t.Errorf("sell = %s, %v; want 50000.5 (up)", got, err)
	}
	if _, err := RoundPrice(m, price, "buy"); err == nil {
		t.Error("an unknown side must be an error, not a guess")
	}
	if got := RoundSize(m, mustDecimal(t, "1.23456")); got.String() != "1.234" {
		t.Errorf("size = %s, want 1.234 (toward zero)", got)
	}
	// A market that serves no tick or lot leaves the value alone.
	if got, _ := RoundPrice(Market{}, price, Buy); got.String() != "50000.30" {
		t.Errorf("no tick = %s, want 50000.30 unchanged", got)
	}
}
