package nexus

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nexus-xyz/nexus-exchange-go/internal/conformance"
	"github.com/nexus-xyz/nexus-exchange-go/internal/transport"
)

// TestConformance is the runtime conformance lane: every row calls one SDK
// method against a live API and joins the bytes it decoded against the pinned
// spec (internal/conformance says how). It is off unless asked for:
//
//	NEXUS_CONFORMANCE=1 go test -run '^TestConformance$' -v .
//
// The public tier runs against testnet with no credential. The rest is opt-in:
//
//   - NEXUS_CONFORMANCE_KEY_ID, NEXUS_CONFORMANCE_KEY_SECRET: an API key (the
//     secret's hex text). Without one, private rows are skipped and no
//     request is sent for them.
//   - NEXUS_CONFORMANCE_REQUIRE_CREDENTIAL=1: a missing credential fails the
//     run instead. The scheduled lane sets it; leave it unset locally.
//   - NEXUS_CONFORMANCE_NETWORK=local: the local stack (docker compose up in
//     the monorepo's eng/apps/exchange) instead of testnet.
//   - NEXUS_CONFORMANCE_ALLOW_WRITES=1: the write tier, local only. It funds
//     the account, rests an order below the mark, reads, amends and cancels
//     it, places a one-order batch and cancels everything in the market.
//   - NEXUS_CONFORMANCE_MARKET: the market to read and write (BTC-USDX-PERP).
func TestConformance(t *testing.T) {
	if os.Getenv("NEXUS_CONFORMANCE") != "1" {
		t.Skip("set NEXUS_CONFORMANCE=1 to run the conformance lane")
	}
	network := Testnet
	if os.Getenv("NEXUS_CONFORMANCE_NETWORK") == "local" {
		network = Local
	}
	writes := os.Getenv("NEXUS_CONFORMANCE_ALLOW_WRITES") == "1"
	if writes && network != Local {
		// Writes stay on a stack built from empty volumes, where an order has
		// nowhere to outlive the run.
		t.Fatal("NEXUS_CONFORMANCE_ALLOW_WRITES runs against the local stack only")
	}
	market := cmp.Or(os.Getenv("NEXUS_CONFORMANCE_MARKET"), "BTC-USDX-PERP")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	spec, err := pinnedSpec(ctx)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse(restBases[network])
	rec := &conformance.Recorder{Prefix: base.Path}
	opts := []Option{WithHTTPClient(&http.Client{Timeout: defaultTimeout, Transport: rec})}
	keyID, secretHex := os.Getenv("NEXUS_CONFORMANCE_KEY_ID"), os.Getenv("NEXUS_CONFORMANCE_KEY_SECRET")
	credential := keyID != "" && secretHex != ""
	if credential {
		secret, err := NewAPISecret(secretHex)
		if err != nil {
			t.Fatal(err)
		}
		opts = append(opts, WithHMACAuth(keyID, secret))
	}
	c, err := NewClient(network, opts...)
	if err != nil {
		t.Fatal(err)
	}

	w := &writeTier{market: market}
	if writes && credential {
		if w.err = w.prepare(ctx, c); w.err != nil {
			t.Errorf("write tier setup: %v", w.err)
		}
	}
	rep := conformance.Run(ctx, conformance.Config{
		Spec:              spec,
		Recorder:          rec,
		Credential:        credential,
		RequireCredential: os.Getenv("NEXUS_CONFORMANCE_REQUIRE_CREDENTIAL") == "1",
		AllowWrites:       writes,
	}, laneOps(c, market, w))
	rep.Drift = goDrift(laneOps(c, market, w), unmeasured, &Client{}, Account{})
	fmt.Printf("Go SDK conformance against %s (%s), credential %v, writes %v\n\n%s", network, restBases[network], credential, writes, rep)
	if rep.Failed() {
		t.Fail()
	}
}

// TestConformanceAccountsForEveryMethod is the Go half of drift, and needs no
// network: every exported method of the REST surface is a row of the lane or
// has a reason in unmeasured, and every name there still exists.
func TestConformanceAccountsForEveryMethod(t *testing.T) {
	for _, d := range goDrift(laneOps(nil, "", nil), unmeasured, &Client{}, Account{}) {
		t.Error(d)
	}
}

// TestConformanceWriteTierPlumbing drives the write tier against a fake venue:
// the order is priced below the mark on the tick, the id each step returns is
// the one the next step acts on, and cancelAllOrders runs last.
func TestConformanceWriteTierPlumbing(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = append(seen, strings.TrimSpace(r.Method+" "+r.URL.RequestURI()+" "+string(body)))
		switch r.Method + " " + r.URL.Path {
		case "GET /markets/BTC/mark-price":
			io.WriteString(w, `{"market_id":"BTC","mark_price":"100000.5"}`)
		case "GET /markets":
			io.WriteString(w, `[{"market_id":"BTC","tick_size":"0.5","min_order_size":"0.001"}]`)
		case "GET /account/cancel-on-disconnect", "PUT /account/cancel-on-disconnect":
			io.WriteString(w, `{"enabled":true,"active":false,"grace_secs":null}`)
		case "POST /orders":
			io.WriteString(w, `{"order":{"id":"o1"},"fills":[]}`)
		case "PATCH /orders/o1":
			io.WriteString(w, `{"id":"o2"}`)
		case "GET /orders/o1", "DELETE /orders/o2":
			io.WriteString(w, `{"id":"`+strings.TrimPrefix(r.URL.Path, "/orders/")+`"}`)
		case "POST /orders/batch", "DELETE /orders":
			io.WriteString(w, `[]`)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()
	spec, err := conformance.ParseSpec([]byte(`{"info":{"version":"test"},"security":[{"hmacAuth":[]}],"paths":{
		"/account/cancel-on-disconnect":{"get":{"operationId":"fetchCancelOnDisconnect"},"put":{"operationId":"setCancelOnDisconnect"}},
		"/orders":{"post":{"operationId":"createOrder"},"delete":{"operationId":"cancelAllOrders"}},
		"/orders/batch":{"post":{"operationId":"createOrdersBatch"}},
		"/orders/{order_id}":{"get":{"operationId":"fetchOrder"},"patch":{"operationId":"editOrder"},"delete":{"operationId":"cancelOrder"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	rec := &conformance.Recorder{Next: srv.Client().Transport}
	c := &Client{t: transport.New(srv.URL, &http.Client{Transport: rec}, APIVersion())}
	ctx := context.Background()
	w := &writeTier{market: "BTC"}
	if err := w.prepare(ctx, c); err != nil {
		t.Fatal(err)
	}
	var ops []conformance.Op
	for _, op := range laneOps(c, "BTC", w) {
		if op.Write || op.ID == "fetchCancelOnDisconnect" {
			ops = append(ops, op)
		}
	}
	seen = nil
	rep := conformance.Run(ctx, conformance.Config{Spec: spec, Recorder: rec, Credential: true, AllowWrites: true}, ops)
	for _, res := range rep.Results {
		if res.Status == conformance.Fail || res.Status == conformance.Skipped {
			t.Errorf("%s: %s %s %s", res.Op.Method, res.Status, res.Category, res.Detail)
		}
	}
	order := `{"market_id":"BTC","order_type":"Limit","price":"97500.0","quantity":"0.001","side":"Buy","time_in_force":"PostOnly"}`
	want := []string{
		"GET /account/cancel-on-disconnect",
		"POST /orders " + order,
		"GET /orders/o1?market_id=BTC",
		`PATCH /orders/o1?market_id=BTC {"price":"96250.0"}`,
		"DELETE /orders/o2?market_id=BTC",
		"POST /orders/batch [" + order + "]",
		`PUT /account/cancel-on-disconnect {"enabled":true}`,
		"DELETE /orders?market_id=BTC",
	}
	if !slices.Equal(seen, want) {
		t.Errorf("requests:\n%s\nwant:\n%s", strings.Join(seen, "\n"), strings.Join(want, "\n"))
	}
}

type driftFixture struct{}

func (driftFixture) Kept()  {}
func (driftFixture) Added() {}

func TestGoDriftBothWays(t *testing.T) {
	got := goDrift([]conformance.Op{{Method: "driftFixture.Kept"}, {Method: "driftFixture.Gone"}},
		map[string]string{"driftFixture.Stale": "a reason"}, driftFixture{})
	want := []string{
		"driftFixture.Added has no row and no reason in unmeasured",
		"driftFixture.Gone is accounted for but does not exist",
		"driftFixture.Stale is accounted for but does not exist",
	}
	if !slices.Equal(got, want) {
		t.Errorf("goDrift = %q, want %q", got, want)
	}
}

// unmeasured is every exported REST-surface method the lane does not call,
// with the reason.
var unmeasured = map[string]string{
	"Client.Account":        "groups the Account methods, which are rows; sends nothing",
	"Client.AccountAddress": "reads GET /account/deposit-target, outside the v1 surface; TestTestnetSignIn checks it",
	"Client.CandleHistory":  "pages fetchOHLCV, which Client.Candles measures; TestTestnetCandleHistory walks it",
	"Client.MarketStream":   "WebSocket: the spec declares no frame schema to join against",
	"Client.Subscribe":      "WebSocket: the spec declares no frame schema to join against",
	"Client.SignIn":         "wallet-signed sign-in; TestTestnetSignIn drives it",
	"Client.CreateAPIKey":   "session auth (bearerAuth), which an API key cannot drive; TestTestnetSignIn does",
	"Client.APIKeys":        "session auth (bearerAuth), which an API key cannot drive; TestTestnetSignIn does",
	"Client.DeleteAPIKey":   "session auth (bearerAuth), which an API key cannot drive; TestTestnetSignIn does",
	"Client.RegisterAgent":  "wallet-signed registration; TestTestnetSignIn drives it",
	"Client.RevokeAgent":    "needs an agent the run registered; TestTestnetSignIn drives it",
}

// laneOps is the lane, in run order. Tier (public or private) is not written
// here: it comes from each operation's security in the pinned spec.
func laneOps(c *Client, market string, w *writeTier) []conformance.Op {
	ops := []conformance.Op{
		{ID: "fetchStatus", Method: "Client.Status", Call: call(func(ctx context.Context) (any, error) { return c.Status(ctx) })},
		{ID: "fetchStats", Method: "Client.Stats", Call: call(func(ctx context.Context) (any, error) { return c.Stats(ctx) })},
		{ID: "fetchStatsHistory", Method: "Client.StatsHistory", Call: call(func(ctx context.Context) (any, error) { return c.StatsHistory(ctx) })},
		{ID: "fetchMarkets", Method: "Client.Markets", Call: call(func(ctx context.Context) (any, error) { return c.Markets(ctx) })},
		{ID: "fetchMarketsSummary", Method: "Client.MarketsSummary", Call: call(func(ctx context.Context) (any, error) { return c.MarketsSummary(ctx) })},
		{ID: "fetchMarkPrice", Method: "Client.MarkPrice", Call: call(func(ctx context.Context) (any, error) { return c.MarkPrice(ctx, market) })},
		{ID: "fetchMarketRiskParams", Method: "Client.MarketRiskParams", Call: call(func(ctx context.Context) (any, error) { return c.MarketRiskParams(ctx, market) })},
		{ID: "fetchMarketStatus", Method: "Client.MarketStatus", Call: call(func(ctx context.Context) (any, error) { return c.MarketStatus(ctx, market) })},
		{ID: "fetchTickers", Method: "Client.Tickers", Call: call(func(ctx context.Context) (any, error) { return c.Tickers(ctx) })},
		{ID: "fetchTicker", Method: "Client.Ticker", Call: call(func(ctx context.Context) (any, error) { return c.Ticker(ctx, market) })},
		{ID: "fetchOrderBook", Method: "Client.OrderBook", Call: call(func(ctx context.Context) (any, error) { return c.OrderBook(ctx, market) })},
		{ID: "fetchTrades", Method: "Client.Trades", Call: func(ctx context.Context) error {
			for _, err := range c.Trades(ctx, market, 5) {
				return err // one page is the measurement
			}
			return nil
		}},
		{ID: "fetchOHLCV", Method: "Client.Candles", Call: call(func(ctx context.Context) (any, error) {
			return c.Candles(ctx, market, CandlesParams{Timeframe: "1m", Limit: 5})
		})},
		{ID: "fetchFunding", Method: "Client.Funding", Call: call(func(ctx context.Context) (any, error) { return c.Funding(ctx, market, 5) })},
		{ID: "fetchFundingSamples", Method: "Client.FundingSamples", Call: call(func(ctx context.Context) (any, error) { return c.FundingSamples(ctx, market, 5) })},
		{ID: "fetchAdlEvents", Method: "Client.AdlEvents", Call: call(func(ctx context.Context) (any, error) { return c.AdlEvents(ctx, market, 5) })},
		{ID: "fetchAccountFunding", Method: "Client.AccountFunding", Call: call(func(ctx context.Context) (any, error) { return c.AccountFunding(ctx, 5) })},
		{ID: "fetchBalance", Method: "Account.Balance", Call: call(func(ctx context.Context) (any, error) { return c.Account().Balance(ctx) })},
		{ID: "fetchPositions", Method: "Client.Positions", Call: call(func(ctx context.Context) (any, error) { return c.Positions(ctx) })},
		{ID: "fetchOpenOrders", Method: "Client.OpenOrders", Call: call(func(ctx context.Context) (any, error) { return c.OpenOrders(ctx) })},
		{ID: "fetchOrderHistory", Method: "Client.OrderHistory", Call: func(ctx context.Context) error {
			_, _, err := c.OrderHistory(ctx, Page{Limit: 5})
			return err
		}},
		{ID: "fetchFills", Method: "Client.Fills", Call: func(ctx context.Context) error {
			_, _, err := c.Fills(ctx, Page{Limit: 5})
			return err
		}},
		{ID: "listAgents", Method: "Client.Agents", Call: call(func(ctx context.Context) (any, error) { return c.Agents(ctx) })},
		{ID: "fetchCancelOnDisconnect", Method: "Account.CancelOnDisconnect", Call: func(ctx context.Context) error {
			s, err := c.Account().CancelOnDisconnect(ctx)
			if err == nil && w != nil {
				w.cod = &s.Enabled
			}
			return err
		}},
	}
	// The write tier. cancelAllOrders is last because it is also the cleanup.
	return append(ops, []conformance.Op{
		{ID: "createOrder", Method: "Client.CreateOrder", Write: true, Call: w.step(func(ctx context.Context) error {
			resp, err := c.CreateOrder(ctx, w.req)
			if err != nil {
				// A band narrower than assumed rejects the order, so name the
				// assumption rather than leave a bare venue error.
				return fmt.Errorf("resting %s at %d bps below mark, inside an assumed %d bps band the spec does not publish: %w",
					w.req.Price, assumedBandBps/2, assumedBandBps, err)
			}
			if resp.Order != nil && resp.Order.Id != nil {
				w.placed = *resp.Order.Id
			}
			return nil
		})},
		{ID: "fetchOrder", Method: "Client.Order", Write: true, Call: w.onPlaced(func(ctx context.Context) error {
			_, err := c.Order(ctx, w.placed, market)
			return err
		})},
		{ID: "editOrder", Method: "Client.EditOrder", Write: true, Call: w.onPlaced(func(ctx context.Context) error {
			o, err := c.EditOrder(ctx, w.placed, market, AmendOrderRequest{Price: &w.amend})
			if err == nil && o.Id != nil {
				w.placed = *o.Id // an amend is a cancel-replace
			}
			return err
		})},
		{ID: "cancelOrder", Method: "Client.CancelOrder", Write: true, Call: w.onPlaced(func(ctx context.Context) error {
			_, err := c.CancelOrder(ctx, w.placed, market)
			return err
		})},
		{ID: "createOrdersBatch", Method: "Client.CreateOrdersBatch", Write: true, Call: w.step(func(ctx context.Context) error {
			_, err := c.CreateOrdersBatch(ctx, []OrderRequest{w.req})
			return err
		})},
		{ID: "setCancelOnDisconnect", Method: "Account.SetCancelOnDisconnect", Write: true, Call: w.step(func(ctx context.Context) error {
			if w.cod == nil {
				return conformance.Skip("the read tier did not report the current setting to write back")
			}
			_, err := c.Account().SetCancelOnDisconnect(ctx, *w.cod) // writes back what it read
			return err
		})},
		{ID: "cancelAllOrders", Method: "Client.CancelAllOrders", Write: true, Call: w.step(func(ctx context.Context) error {
			_, err := c.CancelAllOrders(ctx, market)
			return err
		})},
	}...)
}

func call(f func(context.Context) (any, error)) func(context.Context) error {
	return func(ctx context.Context) error {
		_, err := f(ctx)
		return err
	}
}

// writeTier is the state the write rows share.
type writeTier struct {
	market string
	err    error        // setup failed; every write row skips with it
	req    OrderRequest // a post-only buy that rests below the mark
	amend  Decimal      // further below, for editOrder
	placed string       // the order createOrder placed
	cod    *bool        // the cancel-on-disconnect setting the read tier saw
}

func (w *writeTier) step(f func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		if w.err != nil {
			return conformance.Skip("write tier setup failed: " + w.err.Error())
		}
		return f(ctx)
	}
}

func (w *writeTier) onPlaced(f func(context.Context) error) func(context.Context) error {
	return w.step(func(ctx context.Context) error {
		if w.placed == "" {
			return conformance.Skip("createOrder placed no order to act on")
		}
		return f(ctx)
	})
}

// assumedBandBps is the venue's default order-vs-mark price band. The pinned
// spec publishes no per-market band, so this is an assumption about the venue,
// not a value read from it. If it stops holding, createOrder fails and its
// error says so (see laneOps).
const assumedBandBps = 500

// prepare funds the account and prices an order that rests rather than
// fills: the order rests half of assumedBandBps below the mark and the amend
// three quarters, leaving the rest as headroom for the mark moving while the
// run reads.
func (w *writeTier) prepare(ctx context.Context, c *Client) error {
	// Funding is setup, not measurement: /account/credit has no SDK method.
	// It fails once the day's allowance is claimed, which a funded account
	// does not mind; a real funding gap shows up as createOrder failing.
	_ = c.t.Send(ctx, http.MethodPost, "/account/credit", nil, nil)
	mark, err := c.MarkPrice(ctx, w.market)
	if err != nil {
		return fmt.Errorf("mark price: %w", err)
	}
	markets, err := c.Markets(ctx)
	if err != nil {
		return fmt.Errorf("markets: %w", err)
	}
	i := slices.IndexFunc(markets, func(m Market) bool { return m.MarketId != nil && *m.MarketId == w.market })
	if i < 0 || markets[i].TickSize == nil || markets[i].MinOrderSize == nil {
		return fmt.Errorf("market %s has no tick size or minimum size", w.market)
	}
	tick := *markets[i].TickSize
	price, err := below(mark.MarkPrice, tick, assumedBandBps/2)
	if err != nil {
		return err
	}
	if w.amend, err = below(mark.MarkPrice, tick, assumedBandBps*3/4); err != nil {
		return err
	}
	if price.String() == w.amend.String() {
		return fmt.Errorf("tick %s is too coarse to rest and amend below mark %s", tick, mark.MarkPrice)
	}
	w.req = OrderRequest{MarketId: w.market, OrderType: OrderTypeLimit, Side: Buy, TimeInForce: PostOnly,
		Price: &price, Quantity: *markets[i].MinOrderSize}
	return nil
}

// below is mark less bps basis points, rounded down to tick.
func below(mark, tick Decimal, bps int64) (Decimal, error) {
	m, t := mark.Rat(), tick.Rat()
	if m == nil || t == nil || m.Sign() <= 0 || t.Sign() <= 0 {
		return Decimal{}, fmt.Errorf("cannot price below mark %q on tick %q", mark, tick)
	}
	q := new(big.Rat).Mul(m, big.NewRat(10000-bps, 10000))
	q.Quo(q, t)
	n := new(big.Int).Quo(q.Num(), q.Denom()) // positive, so truncation is floor
	p := new(big.Rat).Mul(new(big.Rat).SetInt(n), t)
	_, frac, _ := strings.Cut(tick.String(), ".")
	return ParseDecimal(p.FloatString(len(frac)))
}

// pinnedSpec fetches the released spec .api-version names, as go generate
// does. The lane judges against the release, never against a working copy.
func pinnedSpec(ctx context.Context) (*conformance.Spec, error) {
	u := "https://github.com/nexus-xyz/nexus-exchange-api/releases/download/" + APIVersion() + "/openapi.json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return conformance.ParseSpec(data)
}

// goDrift checks the lane against the method sets of surfaces, both ways:
// every exported method is a row or has a reason, and every row and reason
// names a method that exists.
func goDrift(ops []conformance.Op, reasons map[string]string, surfaces ...any) []string {
	exists := map[string]bool{}
	for _, s := range surfaces {
		t := reflect.TypeOf(s)
		name := strings.TrimPrefix(t.String(), "*")
		name = name[strings.LastIndex(name, ".")+1:]
		for i := range t.NumMethod() {
			exists[name+"."+t.Method(i).Name] = true
		}
	}
	accounted := map[string]bool{}
	for _, op := range ops {
		accounted[op.Method] = true
	}
	for m := range reasons {
		accounted[m] = true
	}
	var out []string
	for m := range exists {
		if !accounted[m] {
			out = append(out, m+" has no row and no reason in unmeasured")
		}
	}
	for m := range accounted {
		if !exists[m] {
			out = append(out, m+" is accounted for but does not exist")
		}
	}
	slices.Sort(out)
	return out
}
