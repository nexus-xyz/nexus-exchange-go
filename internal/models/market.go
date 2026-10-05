package models

import (
	"encoding/json"
	"errors"
)

// UnmarshalJSON reads a /markets row. The server sends the identifiers under
// CCXT's names, id, base and quote, while the pinned spec declares market_id,
// base_asset and quote_asset. The served names win and the spec's are the
// fallback; the generated fields keep the spec's names, and encoding still
// writes those. A row with neither id nor market_id is an error: a nil
// MarketId would only fail later, as a request to /markets//orderbook
// (ENG-19679).
func (m *Market) UnmarshalJSON(b []byte) error {
	type spec Market // no methods, so no recursion
	var v struct {
		spec
		ID    *string `json:"id"`
		Base  *string `json:"base"`
		Quote *string `json:"quote"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*m = Market(v.spec)
	if v.ID != nil {
		m.MarketId = v.ID
	}
	if v.Base != nil {
		m.BaseAsset = v.Base
	}
	if v.Quote != nil {
		m.QuoteAsset = v.Quote
	}
	if m.MarketId == nil {
		return errors.New("models: market row has neither id nor market_id")
	}
	return nil
}
