package models

import "encoding/json"

// AccountFees The authenticated account's effective fee schedule, mirroring Hyperliquid `userFees`. Reports what the venue charges today: there are no per-account fee tiers or discounts yet (fee model still a draft), so `tier` is `base` and `discounts` is empty. The rate is the forward-looking schedule rate scoped by `schedule`, not a realized per-fill average.
//
// Hand-written, not generated (ENG-21111). The server sends the two rates to
// 0.1 bps (2.8, -0.4) from the monorepo spec that nexus-xyz/nexus#15745
// ships, but the pinned spec still types them integer, which generates an int
// that cannot decode 2.8. The rates here are json.Number, the type the
// generator gives a JSON number (oapi-codegen.yaml). Every other field is as
// generated. Once the pinned spec types the rates number, remove AccountFees
// from exclude-schemas in oapi-codegen.yaml and delete this file.
type AccountFees struct {
	// Discounts Active fee discounts applied to the account. Currently always empty — no discount program exists yet.
	Discounts []FeeDiscount `json:"discounts"`

	// MakerFeeBps Effective maker fee in basis points, to 0.1 bps: an integer for a whole rate (-2), a number with one decimal place for a fractional one (-0.4). Negative means the maker is *paid* a rebate — e.g. -2 is a 0.02% rebate. Positive means the maker pays a fee.
	MakerFeeBps json.Number `json:"maker_fee_bps"`

	// Schedule Scope of the reported rate. Currently always `standard`. The venue charges a per-market schedule (standard crypto, mid-cap crypto, FX, commodities/indices all differ, and the split varies by deploy config), but this endpoint takes no market parameter, so it reports the standard crypto-group schedule and marks it here. Treat the rate as scoped by this value, not a venue-wide guarantee; per-market effective rates are a planned follow-up. Treat as an open string — new scopes may appear.
	Schedule string `json:"schedule"`

	// TakerFeeBps Effective taker fee in basis points, to 0.1 bps: an integer for a whole rate (5, a 0.05% fee), a number with one decimal place for a fractional one (2.8, a 0.028% fee).
	TakerFeeBps json.Number `json:"taker_fee_bps"`

	// Tier Fee tier for the account. Currently always `base`: there are no per-account fee tiers yet (distinct from rate-limit tiers). New values may appear when the fee model lands, so treat this as an open string.
	Tier string `json:"tier"`

	// Volume30d Rolling 30-day traded notional for the account, as a decimal string. Best-effort — see `volume_30d_estimated`.
	Volume30d Decimal `json:"volume_30d"`

	// Volume30dEstimated `true` when `volume_30d` may undercount: the source fill buffer was at capacity, so some older in-window fills may have been evicted. `false` when the full 30-day window is covered.
	Volume30dEstimated bool `json:"volume_30d_estimated"`
}
