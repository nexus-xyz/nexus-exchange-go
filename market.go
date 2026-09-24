package nexus

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/url"
	"strconv"

	"github.com/nexus-xyz/nexus-exchange-go/internal/models"
)

// Market-data types, as the pinned spec defines them.
type (
	// Market is one market's trading parameters: tick and lot size, margin
	// rates, maximum leverage.
	Market = models.Market
	// MarketSummary is one row of [Client.MarketsSummary].
	MarketSummary = models.MarketSummary
	// MarketRiskParams is a market's risk configuration.
	MarketRiskParams = models.MarketRiskParams
	// MarketStatus is whether a market is trading.
	MarketStatus = models.MarketStatus
	// AdlEventRecord is one auto-deleveraging settlement.
	AdlEventRecord = models.AdlEventRecord
	// ServiceHealth is what [Client.Status] returns.
	ServiceHealth = models.ServiceHealth
	// StatsSnapshot is the exchange-wide snapshot [Client.Stats] returns.
	StatsSnapshot = models.StatsSnapshot
	// ThroughputSample is one point of [Client.StatsHistory].
	ThroughputSample = models.ThroughputSample
	// AccountFunding is one funding payment of the authenticated account.
	AccountFunding = models.AccountFunding
	// FundingSample is one settled funding rate of a market.
	FundingSample = models.FundingSample
	// FundingPremiumSample is one premium sample feeding the funding rate.
	FundingPremiumSample = models.FundingPremiumSample
	// Ticker is a CCXT-shaped ticker.
	Ticker = models.Ticker
	// OrderBook is a market's aggregated book.
	OrderBook = models.OrderBook
	// Trade is one public trade, CCXT-shaped (takerOrMaker beside the Nexus
	// is_liquidation).
	Trade = models.Trade
)

// MarkPrice is a market's current mark price. The pinned spec (v0.8.1)
// publishes only an example for this response, so the type is written here
// from it; later specs name it MarkPriceResponse with the same two fields.
type MarkPrice struct {
	MarketID  string  `json:"market_id"`
	MarkPrice Decimal `json:"mark_price"`
}

// Candle is one OHLCV bar. The wire form is a JSON array
// [timestamp, open, high, low, close, volume] of numbers; the prices and
// volume keep their served digits as json.Number, never a float64.
type Candle struct {
	// Timestamp is the bucket start, Unix milliseconds UTC.
	Timestamp                      int64
	Open, High, Low, Close, Volume json.Number
}

func (c *Candle) UnmarshalJSON(b []byte) error {
	var a []json.Number
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	if len(a) != 6 {
		return fmt.Errorf("nexus: candle has %d fields, want 6", len(a))
	}
	ts, err := a[0].Int64()
	if err != nil {
		return fmt.Errorf("nexus: candle timestamp %q: %w", a[0], err)
	}
	*c = Candle{ts, a[1], a[2], a[3], a[4], a[5]}
	return nil
}

func (c Candle) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{c.Timestamp, c.Open, c.High, c.Low, c.Close, c.Volume})
}

// CandlesParams selects candles. Zero fields are not sent, and the server
// applies its default. Nothing is validated locally: an out-of-range Limit is
// sent as is, and the server clamps it.
type CandlesParams struct {
	// Timeframe is the bar width: "1s", "1m", "5m" or "1h". Default "1m".
	Timeframe string
	// Limit is the most bars one response holds.
	Limit int
	// StartTime and EndTime bound the window, inclusive, in Unix ms. With
	// StartTime the server returns the earliest Limit bars at or after it;
	// without, the latest Limit bars up to EndTime (or now).
	StartTime, EndTime int64
}

func (p CandlesParams) query() url.Values {
	q := url.Values{}
	if p.Timeframe != "" {
		q.Set("timeframe", p.Timeframe)
	}
	setInt(q, "limit", int64(p.Limit))
	setInt(q, "startTime", p.StartTime)
	setInt(q, "endTime", p.EndTime)
	return q
}

// setInt sets key to v unless v is zero. No range check: limit clamps
// server-side, and a local error the server would not return is worse.
func setInt(q url.Values, key string, v int64) {
	if v != 0 {
		q.Set(key, strconv.FormatInt(v, 10))
	}
}

func limitQuery(limit int) url.Values {
	q := url.Values{}
	setInt(q, "limit", int64(limit))
	return q
}

func marketPath(marketID, rest string) string {
	return "/markets/" + url.PathEscape(marketID) + rest
}

func getJSON[T any](ctx context.Context, c *Client, path string, q url.Values) (T, error) {
	var v T
	err := c.t.Get(ctx, path, q, &v)
	return v, err
}

// Markets lists every market's trading parameters (GET /markets).
func (c *Client) Markets(ctx context.Context) ([]Market, error) {
	return getJSON[[]Market](ctx, c, "/markets", nil)
}

// MarketsSummary lists every market with its last price and 24h activity
// (GET /markets/summary).
func (c *Client) MarketsSummary(ctx context.Context) ([]MarketSummary, error) {
	return getJSON[[]MarketSummary](ctx, c, "/markets/summary", nil)
}

// MarkPrice returns a market's current mark price
// (GET /markets/{market_id}/mark-price).
func (c *Client) MarkPrice(ctx context.Context, marketID string) (*MarkPrice, error) {
	return getJSON[*MarkPrice](ctx, c, marketPath(marketID, "/mark-price"), nil)
}

// MarketRiskParams returns a market's risk configuration
// (GET /markets/{market_id}/risk-params).
func (c *Client) MarketRiskParams(ctx context.Context, marketID string) (*MarketRiskParams, error) {
	return getJSON[*MarketRiskParams](ctx, c, marketPath(marketID, "/risk-params"), nil)
}

// MarketStatus returns whether a market is trading
// (GET /markets/{market_id}/status).
func (c *Client) MarketStatus(ctx context.Context, marketID string) (*MarketStatus, error) {
	return getJSON[*MarketStatus](ctx, c, marketPath(marketID, "/status"), nil)
}

// AdlEvents returns up to limit ADL settlements for a market, newest first
// (GET /markets/{market_id}/adl-events). Zero limit leaves it to the server.
//
// The server requires a credential for this read: on a client without one it
// returns a 401 [*APIError].
func (c *Client) AdlEvents(ctx context.Context, marketID string, limit int) ([]AdlEventRecord, error) {
	return getJSON[[]AdlEventRecord](ctx, c, marketPath(marketID, "/adl-events"), limitQuery(limit))
}

// Status returns the service's health (GET /status).
func (c *Client) Status(ctx context.Context) (*ServiceHealth, error) {
	return getJSON[*ServiceHealth](ctx, c, "/status", nil)
}

// Stats returns the exchange-wide statistics snapshot (GET /stats).
func (c *Client) Stats(ctx context.Context) (*StatsSnapshot, error) {
	return getJSON[*StatsSnapshot](ctx, c, "/stats", nil)
}

// StatsHistory returns recent throughput samples (GET /stats/history).
func (c *Client) StatsHistory(ctx context.Context) ([]ThroughputSample, error) {
	return getJSON[[]ThroughputSample](ctx, c, "/stats/history", nil)
}

// AccountFunding returns up to limit funding payments of the authenticated
// account, newest first (GET /funding). Zero limit leaves it to the server.
//
// The server requires a credential for this read: on a client without one it
// returns a 401 [*APIError].
func (c *Client) AccountFunding(ctx context.Context, limit int) ([]AccountFunding, error) {
	return getJSON[[]AccountFunding](ctx, c, "/funding", limitQuery(limit))
}

// Funding returns up to limit settled funding rates for a market
// (GET /markets/{market_id}/funding). Zero limit leaves it to the server.
func (c *Client) Funding(ctx context.Context, marketID string, limit int) ([]FundingSample, error) {
	return getJSON[[]FundingSample](ctx, c, marketPath(marketID, "/funding"), limitQuery(limit))
}

// FundingSamples returns up to limit premium samples for a market
// (GET /markets/{market_id}/funding-samples). Zero limit leaves it to the
// server.
func (c *Client) FundingSamples(ctx context.Context, marketID string, limit int) ([]FundingPremiumSample, error) {
	return getJSON[[]FundingPremiumSample](ctx, c, marketPath(marketID, "/funding-samples"), limitQuery(limit))
}

// Tickers returns every market's ticker, keyed by market id (GET /tickers).
func (c *Client) Tickers(ctx context.Context) (map[string]Ticker, error) {
	return getJSON[map[string]Ticker](ctx, c, "/tickers", nil)
}

// Ticker returns one market's ticker (GET /markets/{market_id}/ticker).
func (c *Client) Ticker(ctx context.Context, marketID string) (*Ticker, error) {
	return getJSON[*Ticker](ctx, c, marketPath(marketID, "/ticker"), nil)
}

// OrderBook returns a market's book (GET /markets/{market_id}/orderbook).
func (c *Client) OrderBook(ctx context.Context, marketID string) (*OrderBook, error) {
	return getJSON[*OrderBook](ctx, c, marketPath(marketID, "/orderbook"), nil)
}

// Trades iterates a market's trades, following the server's cursor from page
// to page (GET /markets/{market_id}/trades). limit is the page size, sent
// unvalidated; zero leaves it to the server. Break out of the loop to stop.
//
//	for t, err := range client.Trades(ctx, "BTC-USDX-PERP", 500) {
//		if err != nil {
//			return err
//		}
//		...
//	}
func (c *Client) Trades(ctx context.Context, marketID string, limit int) iter.Seq2[Trade, error] {
	return paginate[Trade](ctx, c, marketPath(marketID, "/trades"), limitQuery(limit))
}

// Candles returns one response of OHLCV bars, ascending by timestamp
// (GET /markets/{market_id}/candles). To read further back than one
// response holds, use [Client.CandleHistory].
//
// StartTime and EndTime are not in the pinned spec (v0.8.1); the server
// accepts them and later specs document them.
func (c *Client) Candles(ctx context.Context, marketID string, p CandlesParams) ([]Candle, error) {
	return getJSON[[]Candle](ctx, c, marketPath(marketID, "/candles"), p.query())
}

// CandleHistory walks a market's candles backwards through history, newest
// first, one request per p.Limit bars. It starts at p.EndTime (zero: now) and
// each request ends just before the oldest bar the last one returned. It
// stops when the server has no older bars, when it passes p.StartTime (if
// set; StartTime is never sent, because it would flip the server's paging
// direction to forwards), or when the caller breaks.
//
// A server that ignores endTime would serve the same page forever; the walk
// detects that and yields an error instead.
func (c *Client) CandleHistory(ctx context.Context, marketID string, p CandlesParams) iter.Seq2[Candle, error] {
	return func(yield func(Candle, error) bool) {
		stop := p.StartTime
		p.StartTime = 0
		for {
			bars, err := c.Candles(ctx, marketID, p)
			if err != nil {
				yield(Candle{}, err)
				return
			}
			if len(bars) == 0 {
				return
			}
			if p.EndTime != 0 && bars[len(bars)-1].Timestamp > p.EndTime {
				yield(Candle{}, fmt.Errorf("nexus: candles for %s: server ignored endTime %d", marketID, p.EndTime))
				return
			}
			for i := len(bars) - 1; i >= 0; i-- {
				if bars[i].Timestamp < stop {
					return
				}
				if !yield(bars[i], nil) {
					return
				}
			}
			oldest := bars[0].Timestamp
			if oldest <= 0 {
				return
			}
			p.EndTime = oldest - 1
		}
	}
}
