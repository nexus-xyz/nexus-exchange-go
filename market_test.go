package nexus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nexus-xyz/nexus-exchange-go/internal/transport"
)

// newTestClient points a credential-free client at h.
func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Client{t: transport.New(srv.URL, srv.Client(), APIVersion())}
}

// TestPublicOperations reaches every market-data method through a client
// with no credential, checks the request on the wire, and decodes a body.
func TestPublicOperations(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, uri, body string
		call            func(*Client) error
	}{
		{"Markets", "/markets", `[{"market_id":"BTC-USDX-PERP","tick_size":"0.5"}]`,
			func(c *Client) error {
				v, err := c.Markets(ctx)
				return want(err, len(v) == 1 && v[0].TickSize.String() == "0.5")
			}},
		{"MarketsSummary", "/markets/summary", `[{"market_id":"BTC-USDX-PERP","last_trade_price":null,"volume_24h":0.0}]`,
			func(c *Client) error { v, err := c.MarketsSummary(ctx); return want(err, len(v) == 1) }},
		{"MarkPrice", "/markets/BTC-USDX-PERP/mark-price", `{"market_id":"BTC-USDX-PERP","mark_price":"50011.60"}`,
			func(c *Client) error {
				v, err := c.MarkPrice(ctx, "BTC-USDX-PERP")
				return want(err, v != nil && v.MarkPrice.String() == "50011.60")
			}},
		{"MarketRiskParams", "/markets/BTC-USDX-PERP/risk-params", `{}`,
			func(c *Client) error { v, err := c.MarketRiskParams(ctx, "BTC-USDX-PERP"); return want(err, v != nil) }},
		{"MarketStatus", "/markets/BTC-USDX-PERP/status", `{"market_id":"BTC-USDX-PERP","status":"active"}`,
			func(c *Client) error { v, err := c.MarketStatus(ctx, "BTC-USDX-PERP"); return want(err, v != nil) }},
		{"AdlEvents", "/markets/BTC-USDX-PERP/adl-events?limit=7", `[]`,
			func(c *Client) error { _, err := c.AdlEvents(ctx, "BTC-USDX-PERP", 7); return err }},
		{"Status", "/status", `{"status":"ok"}`,
			func(c *Client) error { v, err := c.Status(ctx); return want(err, v != nil) }},
		{"Stats", "/stats", `{}`,
			func(c *Client) error { v, err := c.Stats(ctx); return want(err, v != nil) }},
		{"StatsHistory", "/stats/history", `[{}]`,
			func(c *Client) error { v, err := c.StatsHistory(ctx); return want(err, len(v) == 1) }},
		{"AccountFunding", "/funding", `[]`,
			func(c *Client) error { _, err := c.AccountFunding(ctx, 0); return err }},
		{"Funding", "/markets/BTC-USDX-PERP/funding?limit=3", `[{}]`,
			func(c *Client) error { v, err := c.Funding(ctx, "BTC-USDX-PERP", 3); return want(err, len(v) == 1) }},
		{"FundingSamples", "/markets/BTC-USDX-PERP/funding-samples", `[{}]`,
			func(c *Client) error {
				v, err := c.FundingSamples(ctx, "BTC-USDX-PERP", 0)
				return want(err, len(v) == 1)
			}},
		{"Tickers", "/tickers", `{"BTC-USDX-PERP":{"symbol":"BTC-USDX-PERP"},"ETH-USDX-PERP":{}}`,
			func(c *Client) error { v, err := c.Tickers(ctx); return want(err, len(v) == 2) }},
		{"Ticker", "/markets/BTC-USDX-PERP/ticker", `{"symbol":"BTC-USDX-PERP"}`,
			func(c *Client) error { v, err := c.Ticker(ctx, "BTC-USDX-PERP"); return want(err, v != nil) }},
		// A book level is untyped in the spec; its numbers must still decode
		// as json.Number, never float64.
		{"OrderBook", "/markets/BTC-USDX-PERP/orderbook", `{"bids":[[78227.10,0.300]],"asks":[]}`,
			func(c *Client) error {
				v, err := c.OrderBook(ctx, "BTC-USDX-PERP")
				return want(err, v != nil && (*v.Bids)[0][0] == json.Number("78227.10") && (*v.Bids)[0][1] == json.Number("0.300"))
			}},
		{"Trades", "/markets/BTC-USDX-PERP/trades", `[{"id":"t1","price":65000.10,"is_liquidation":false}]`,
			func(c *Client) error {
				n := 0
				for tr, err := range c.Trades(ctx, "BTC-USDX-PERP", 0) {
					if err != nil {
						return err
					}
					n++
					if tr.Price.String() != "65000.10" {
						return fmt.Errorf("price %s", tr.Price)
					}
				}
				return want(nil, n == 1)
			}},
		{"Candles", "/markets/BTC-USDX-PERP/candles?endTime=9&limit=2&timeframe=5m",
			`[[1789000560000,78227.0,78405.0,78140.5,78225.0,26.555]]`,
			func(c *Client) error {
				v, err := c.Candles(ctx, "BTC-USDX-PERP", CandlesParams{Timeframe: "5m", Limit: 2, EndTime: 9})
				return want(err, len(v) == 1 && v[0].Timestamp == 1789000560000 && v[0].Open == "78227.0" && v[0].Volume == "26.555")
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.RequestURI() != tc.uri {
					t.Errorf("%s %s, want GET %s", r.Method, r.URL.RequestURI(), tc.uri)
				}
				for _, h := range []string{"X-Api-Key", "X-Signature", "X-Timestamp", "Authorization"} {
					if r.Header.Get(h) != "" {
						t.Errorf("unauthenticated request carries %s", h)
					}
				}
				w.Write([]byte(tc.body))
			})
			if err := tc.call(c); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func want(err error, ok bool) error {
	if err == nil && !ok {
		return errors.New("unexpected decoded value")
	}
	return err
}

// TestTradesFollowsHeaderCursor walks three pages of the X-Next-Cursor shape.
// The cursors are deliberately unparseable-looking: they must round-trip
// byte for byte, and limit must ride on every page.
func TestTradesFollowsHeaderCursor(t *testing.T) {
	pages := map[string]struct{ body, next string }{
		"":               {`[{"id":"1"},{"id":"2"}]`, "a b/c==&x"},
		"a b/c==&x":      {`[{"id":"3"}]`, `{"not":"json"}`},
		`{"not":"json"}`: {`[{"id":"4"}]`, ""},
	}
	var requests atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if got := r.URL.Query().Get("limit"); got != "2" {
			t.Errorf("limit = %q, want 2", got)
		}
		p, ok := pages[r.URL.Query().Get("cursor")]
		if !ok {
			t.Errorf("unknown cursor %q", r.URL.Query().Get("cursor"))
			return
		}
		if p.next != "" {
			w.Header().Set("X-Next-Cursor", p.next)
		}
		w.Write([]byte(p.body))
	})
	var ids []string
	for tr, err := range c.Trades(context.Background(), "BTC-USDX-PERP", 2) {
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, *tr.Id)
	}
	if strings.Join(ids, ",") != "1,2,3,4" || requests.Load() != 3 {
		t.Fatalf("ids %v in %d requests", ids, requests.Load())
	}
}

// TestPaginateEnvelope walks the {"items", "next_cursor"} body shape through
// the same iterator, and checks a stale header does not override the body.
func TestPaginateEnvelope(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Next-Cursor", "ignored-when-the-body-is-an-envelope")
		switch r.URL.Query().Get("cursor") {
		case "":
			w.Write([]byte(`{"items":[1,2],"next_cursor":"opaque:1"}`))
		case "opaque:1":
			w.Write([]byte(`{"items":[3],"next_cursor":null}`))
		default:
			t.Errorf("cursor %q", r.URL.Query().Get("cursor"))
		}
	})
	var got []int
	for n, err := range paginate[int](context.Background(), c, "/things", nil) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, n)
	}
	if fmt.Sprint(got) != "[1 2 3]" {
		t.Fatalf("got %v", got)
	}
}

func TestPaginateStopsOnRepeatedCursor(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Next-Cursor", "same")
		w.Write([]byte(`[1]`))
	})
	var n int
	var last error
	for _, err := range paginate[int](context.Background(), c, "/things", nil) {
		n++
		last = err
	}
	if last == nil || n != 3 { // item, item, error
		t.Fatalf("n=%d err=%v", n, last)
	}
}

// TestLimitPassesThrough: the SDK never range-checks limit. The server clamps
// out-of-range values, so a local error would be one it never returns.
func TestLimitPassesThrough(t *testing.T) {
	var mu sync.Mutex
	var got []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.URL.Query()["limit"]...)
		mu.Unlock()
		w.Write([]byte(`[]`))
	})
	ctx := context.Background()
	for _, limit := range []int{-1, 5000, 1} {
		if _, err := c.Funding(ctx, "BTC-USDX-PERP", limit); err != nil {
			t.Fatal(err)
		}
		for _, err := range c.Trades(ctx, "BTC-USDX-PERP", limit) {
			if err != nil {
				t.Fatal(err)
			}
		}
		if _, err := c.Candles(ctx, "BTC-USDX-PERP", CandlesParams{Limit: limit}); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if want := "-1 -1 -1 5000 5000 5000 1 1 1"; strings.Join(got, " ") != want {
		t.Fatalf("limits sent %v, want %s", got, want)
	}
}

// candleServer serves bars at every minute in [first, last] with the server's
// candle semantics: ascending, bounded by endTime inclusive, the latest limit
// bars when startTime is absent.
func candleServer(t *testing.T, first, last int64, requests *atomic.Int32) *Client {
	return newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		q := r.URL.Query()
		if q.Get("startTime") != "" {
			t.Error("backward walk sent startTime, which flips paging forwards")
		}
		limit, _ := strconv.ParseInt(q.Get("limit"), 10, 64)
		end := last
		if e := q.Get("endTime"); e != "" {
			end, _ = strconv.ParseInt(e, 10, 64)
		}
		end -= ((end-first)%60000 + 60000) % 60000 // round down onto a bar
		var bars []string
		for ts := max(first, end-(limit-1)*60000); ts <= end; ts += 60000 {
			bars = append(bars, fmt.Sprintf("[%d,1.0,2.0,0.5,1.5,10.25]", ts))
		}
		fmt.Fprintf(w, "[%s]", strings.Join(bars, ","))
	})
}

// TestCandleHistoryWalksBackwards is the candles criterion offline: 250 bars,
// 100 per response, walked newest to oldest with no gap or repeat.
func TestCandleHistoryWalksBackwards(t *testing.T) {
	const first, n = int64(1_788_000_000_000), 250
	last := first + (n-1)*60000
	var requests atomic.Int32
	c := candleServer(t, first, last, &requests)
	prev, count := int64(1<<62), 0
	for bar, err := range c.CandleHistory(context.Background(), "BTC-USDX-PERP", CandlesParams{Limit: 100}) {
		if err != nil {
			t.Fatal(err)
		}
		if bar.Timestamp != prev-60000 && count > 0 {
			t.Fatalf("bar %d at %d follows %d: gap or repeat", count, bar.Timestamp, prev)
		}
		prev = bar.Timestamp
		count++
	}
	// Three full-or-partial pages, then one empty response ends the walk.
	if count != n || prev != first || requests.Load() != 4 {
		t.Fatalf("walked %d bars to %d in %d requests; want %d to %d in 4", count, prev, requests.Load(), n, first)
	}

	// StartTime is a client-side stop bound, not a query parameter.
	requests.Store(0)
	count = 0
	stop := last - 149*60000
	for bar, err := range c.CandleHistory(context.Background(), "BTC-USDX-PERP", CandlesParams{Limit: 100, StartTime: stop}) {
		if err != nil || bar.Timestamp < stop {
			t.Fatalf("bar %d err %v", bar.Timestamp, err)
		}
		count++
	}
	if count != 150 || requests.Load() != 2 {
		t.Fatalf("stopped after %d bars, %d requests; want 150, 2", count, requests.Load())
	}
}

// TestCandleHistoryDetectsIgnoredEndTime is the failure that burned a gate
// before: a server that ignores the bound serves the same page forever.
func TestCandleHistoryDetectsIgnoredEndTime(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[[1000,1,1,1,1,1],[61000,1,1,1,1,1]]`))
	})
	var n int
	var last error
	for _, err := range c.CandleHistory(context.Background(), "BTC-USDX-PERP", CandlesParams{}) {
		n++
		last = err
	}
	if last == nil || n != 3 {
		t.Fatalf("n=%d err=%v; want two bars then an error", n, last)
	}
}

func TestVersionError(t *testing.T) {
	for _, tc := range []struct {
		name, body, header, min string
	}{
		{"body", `{"code":"api_version_unsupported","message":"Unsupported","min_version":"0.9.0",` +
			`"current_version":"0.9.83","spec_url":"https://example.test/spec"}`, "", "0.9.0"},
		{"header only", `{"code":"api_version_unsupported"}`, "0.9.1", "0.9.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if tc.header != "" {
					w.Header().Set("X-Nexus-Api-Min-Version", tc.header)
				}
				w.WriteHeader(http.StatusUpgradeRequired)
				w.Write([]byte(tc.body))
			})
			_, err := c.Status(context.Background())
			var verr *VersionError
			if !errors.As(err, &verr) {
				t.Fatalf("err = %v, want *VersionError", err)
			}
			if verr.Pinned != APIVersion() || verr.Min != tc.min {
				t.Errorf("pinned %q min %q", verr.Pinned, verr.Min)
			}
			if tc.name == "body" && (verr.Current != "0.9.83" || verr.SpecURL != "https://example.test/spec") {
				t.Errorf("current %q spec %q", verr.Current, verr.SpecURL)
			}
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != 426 || apiErr.Code != "api_version_unsupported" {
				t.Errorf("APIError = %+v", apiErr)
			}
			if !strings.Contains(err.Error(), APIVersion()) || requests.Load() != 1 {
				t.Errorf("%q after %d requests", err, requests.Load())
			}
		})
	}
}

// TestTestnetCandleHistory runs the backward walk against testnet's real
// candles. Set NEXUS_TESTNET=1 to run it; it needs no credential.
func TestTestnetCandleHistory(t *testing.T) {
	if os.Getenv("NEXUS_TESTNET") != "1" {
		t.Skip("set NEXUS_TESTNET=1 to run against api.testnet.nexus.xyz")
	}
	c, err := NewClient(Testnet)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	const limit, want = 100, 350
	var bars []Candle
	for bar, err := range c.CandleHistory(ctx, "BTC-USDX-PERP", CandlesParams{Timeframe: "1m", Limit: limit}) {
		if err != nil {
			t.Fatal(err)
		}
		if n := len(bars); n > 0 && bar.Timestamp >= bars[n-1].Timestamp {
			t.Fatalf("bar %d at %d does not precede %d", n, bar.Timestamp, bars[n-1].Timestamp)
		}
		if bars = append(bars, bar); len(bars) == want {
			break
		}
	}
	if len(bars) <= limit {
		t.Fatalf("walked %d bars; want more than one page (%d)", len(bars), limit)
	}
	ms := func(ts int64) string { return time.UnixMilli(ts).UTC().Format(time.RFC3339) }
	t.Logf("walked %d bars backwards, %s down to %s", len(bars), ms(bars[0].Timestamp), ms(bars[len(bars)-1].Timestamp))
}
