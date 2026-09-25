package nexus

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nexus-xyz/nexus-exchange-go/internal/signing"
	"github.com/nexus-xyz/nexus-exchange-go/internal/transport"
)

// testClient returns a signing client for an httptest server running h.
func testClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	tr := transport.New(srv.URL, srv.Client(), APIVersion())
	tr.Signer = &signing.HMAC{KeyID: "k", Secret: make([]byte, 32)}
	return &Client{t: tr}
}

// seen is what one request carried.
type seen struct {
	method, uri, body string
	signed            bool
}

// recorder answers every request with status and resp, and records it.
// The returned func reports the requests so far.
func recorder(t *testing.T, status int, resp string) (*Client, func() []seen) {
	var mu sync.Mutex
	var got []seen
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, seen{r.Method, r.URL.RequestURI(), string(b), r.Header.Get("X-Signature") != ""})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Next-Cursor", "next-1")
		w.WriteHeader(status)
		io.WriteString(w, resp)
	})
	return c, func() []seen {
		mu.Lock()
		defer mu.Unlock()
		return append([]seen(nil), got...)
	}
}

func mustDecimal(t *testing.T, s string) Decimal {
	t.Helper()
	d, err := ParseDecimal(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

const orderJSON = `{"id":"o1","market_id":"BTC-USDX-PERP","side":"Buy","order_type":"Limit","price":"50000.10",` +
	`"quantity":"0.010","filled_qty":"0","status":"Open","time_in_force":"GTC","created_at":1,"updated_at":2}`

// TestOperationsOnTheWire pins each operation's method, path, query and body,
// and that every one is signed.
func TestOperationsOnTheWire(t *testing.T) {
	ctx := context.Background()
	price := mustDecimal(t, "50000.10")
	req := OrderRequest{MarketId: "BTC-USDX-PERP", Side: Buy, OrderType: OrderTypeLimit,
		Price: &price, Quantity: mustDecimal(t, "0.010"), TimeInForce: GTC}
	for _, tc := range []struct {
		name, resp, method, uri, body string
		call                          func(*Client) error
	}{
		{"createOrder", `{"order":` + orderJSON + `,"fills":[]}`, "POST", "/orders",
			`{"market_id":"BTC-USDX-PERP","side":"Buy","order_type":"Limit","price":"50000.10","quantity":"0.010","time_in_force":"GTC"}`,
			func(c *Client) error {
				r, err := c.CreateOrder(ctx, req)
				if err == nil && (r.Order.Price.String() != "50000.10" || *r.Order.Id != "o1") {
					t.Errorf("createOrder decoded %+v", r.Order)
				}
				return err
			}},
		{"editOrder", orderJSON, "PATCH", "/orders/o%2F1?market_id=BTC-USDX-PERP", `{"price":"1.5"}`,
			func(c *Client) error {
				p := mustDecimal(t, "1.5")
				_, err := c.EditOrder(ctx, "o/1", "BTC-USDX-PERP", AmendOrderRequest{Price: &p})
				return err
			}},
		{"cancelOrder", orderJSON, "DELETE", "/orders/o1?market_id=BTC-USDX-PERP", "",
			func(c *Client) error { _, err := c.CancelOrder(ctx, "o1", "BTC-USDX-PERP"); return err }},
		{"cancelAllOrders", "[" + orderJSON + "]", "DELETE", "/orders", "",
			func(c *Client) error {
				o, err := c.CancelAllOrders(ctx, "")
				if err == nil && len(o) != 1 {
					t.Errorf("cancelAll decoded %d orders", len(o))
				}
				return err
			}},
		{"cancelAllOrders market", "[]", "DELETE", "/orders?market_id=ETH-USDX-PERP", "",
			func(c *Client) error { _, err := c.CancelAllOrders(ctx, "ETH-USDX-PERP"); return err }},
		{"fetchOrder", orderJSON, "GET", "/orders/o1?market_id=BTC-USDX-PERP", "",
			func(c *Client) error { _, err := c.FetchOrder(ctx, "o1", "BTC-USDX-PERP"); return err }},
		{"fetchOpenOrders", "[" + orderJSON + "]", "GET", "/orders", "",
			func(c *Client) error { _, err := c.FetchOpenOrders(ctx); return err }},
		{"fetchOrderHistory", `[{"id":"o1","status":"Filled","price":null}]`, "GET", "/orders/history?cursor=c0&limit=50", "",
			func(c *Client) error {
				h, next, err := c.FetchOrders(ctx, Page{Limit: 50, Cursor: "c0"})
				if err == nil && (next != "next-1" || len(h) != 1 || !h[0].Price.IsNull()) {
					t.Errorf("history = %+v, next %q", h, next)
				}
				return err
			}},
		{"fetchFills", `[{"id":"f1","price":"84250.00","size":"0.01","fee":"0.84","taker_or_maker":"taker"}]`, "GET", "/fills", "",
			func(c *Client) error {
				f, next, err := c.FetchMyTrades(ctx, Page{})
				if err == nil && (next != "next-1" || f[0].Fee.String() != "0.84") {
					t.Errorf("fills = %+v, next %q", f, next)
				}
				return err
			}},
		{"fetchBalance", `{"balance":"1000.00","equity":"1012.34","positions":[]}`, "GET", "/account", "",
			func(c *Client) error {
				a, err := c.Account().FetchBalance(ctx)
				if err == nil && a.Equity.String() != "1012.34" {
					t.Errorf("balance = %+v", a)
				}
				return err
			}},
		{"fetchPositions", `[{"market_id":"BTC-USDX-PERP","side":"Long","size":"0.5","roe":null,"roe_error":"mark_unavailable"}]`, "GET", "/positions", "",
			func(c *Client) error {
				p, err := c.FetchPositions(ctx)
				if err == nil && (!p[0].Roe.IsNull() || p[0].RoeError.MustGet() != "mark_unavailable") {
					t.Errorf("positions = %+v", p)
				}
				return err
			}},
		{"fetchCancelOnDisconnect", `{"enabled":false,"active":false,"grace_secs":null}`, "GET", "/account/cancel-on-disconnect", "",
			func(c *Client) error { _, err := c.Account().FetchCancelOnDisconnect(ctx); return err }},
		{"setCancelOnDisconnect", `{"enabled":true,"active":true,"grace_secs":10}`, "PUT", "/account/cancel-on-disconnect", `{"enabled":true}`,
			func(c *Client) error {
				s, err := c.Account().SetCancelOnDisconnect(ctx, true)
				if err == nil && (!s.Active || s.GraceSecs.MustGet() != 10) {
					t.Errorf("cod = %+v", s)
				}
				return err
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, got := recorder(t, 200, tc.resp)
			if err := tc.call(c); err != nil {
				t.Fatal(err)
			}
			if len(got()) != 1 {
				t.Fatalf("%d requests, want 1", len(got()))
			}
			g := got()[0]
			if g.method != tc.method || g.uri != tc.uri || !g.signed {
				t.Errorf("sent %s %s (signed %v), want %s %s", g.method, g.uri, g.signed, tc.method, tc.uri)
			}
			if tc.body == "" && g.body != "" || tc.body != "" && !jsonEqual(t, g.body, tc.body) {
				t.Errorf("body %s, want %s", g.body, tc.body)
			}
		})
	}
}

func jsonEqual(t *testing.T, a, b string) bool {
	t.Helper()
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		return false
	}
	xb, _ := json.Marshal(x)
	yb, _ := json.Marshal(y)
	return string(xb) == string(yb)
}

// TestMutationsNeverRetried: a 503 on any state-changing call is returned
// after exactly one request. A resubmitted order is worse than a failed one.
func TestMutationsNeverRetried(t *testing.T) {
	ctx := context.Background()
	for name, call := range map[string]func(*Client) error{
		"create": func(c *Client) error { _, err := c.CreateOrder(ctx, OrderRequest{}); return err },
		"batch":  func(c *Client) error { _, err := c.CreateOrders(ctx, []OrderRequest{{}}); return err },
		"edit":   func(c *Client) error { _, err := c.EditOrder(ctx, "o1", "m", AmendOrderRequest{}); return err },
		"cancel": func(c *Client) error { _, err := c.CancelOrder(ctx, "o1", "m"); return err },
		"cancelAll": func(c *Client) error {
			_, err := c.CancelAllOrders(ctx, "")
			return err
		},
		"setCOD": func(c *Client) error { _, err := c.Account().SetCancelOnDisconnect(ctx, true); return err },
	} {
		for status, body := range map[int]string{
			503: `{"code":"unavailable","message":"try later"}`,
			429: `{"code":"RATE_LIMIT_EXCEEDED","message":"slow down","bucket":"order"}`,
		} {
			t.Run(name+"/"+strconv.Itoa(status), func(t *testing.T) {
				c, got := recorder(t, status, body)
				err := call(c)
				var apiErr *APIError
				if !errors.As(err, &apiErr) || apiErr.StatusCode != status {
					t.Fatalf("err = %v, want a %d APIError", err, status)
				}
				if len(got()) != 1 {
					t.Fatalf("sent %d times, want exactly 1", len(got()))
				}
			})
		}
	}
}

// TestCancelNotDelayedBySubmissionBacklog: with the order budget spent and a
// backlog of submissions waiting on it, a cancel still goes out at once. The
// client has no cancel budget to wait on, and never borrows the order one.
func TestCancelNotDelayedBySubmissionBacklog(t *testing.T) {
	var creates atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		label := "cancel"
		if r.Method == http.MethodPost {
			creates.Add(1)
			label = "order"
		}
		// Every budget reports itself spent, at one token a second.
		w.Header().Set("X-Ratelimit-Limit", "1")
		w.Header().Set("X-Ratelimit-Remaining", "0")
		w.Header().Set("X-Ratelimit-Bucket", label)
		io.WriteString(w, `{"order":`+orderJSON+`,"fills":[]}`)
	})
	if _, err := c.CreateOrder(context.Background(), OrderRequest{}); err != nil {
		t.Fatal(err)
	}

	// Five more submissions queue behind the spent order budget.
	backlog, stop := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.CreateOrder(backlog, OrderRequest{})
		}()
	}
	defer func() { stop(); wg.Wait() }()
	time.Sleep(50 * time.Millisecond)

	for i := range 3 {
		start := time.Now()
		if _, err := c.CancelOrder(context.Background(), "o1", "BTC-USDX-PERP"); err != nil {
			t.Fatal(err)
		}
		if d := time.Since(start); d > 200*time.Millisecond {
			t.Fatalf("cancel %d took %v behind a submission backlog", i+1, d)
		}
	}
	if n := creates.Load(); n != 1 {
		t.Fatalf("%d creates reached the server; the backlog was not held, so the test proved nothing", n)
	}
}

// TestCancelNotQueuedBehindCreate: with a create held open by the server, a
// cancel on the same client still completes. Nothing in the SDK serializes
// requests, so risk reduction never waits on a submission.
func TestCancelNotQueuedBehindCreate(t *testing.T) {
	createArrived := make(chan struct{})
	release := make(chan struct{})
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			close(createArrived)
			<-release
		}
		io.WriteString(w, orderJSON)
	})
	defer close(release)

	createDone := make(chan error, 1)
	go func() {
		_, err := c.CreateOrder(context.Background(), OrderRequest{})
		createDone <- err
	}()
	<-createArrived

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := c.CancelOrder(ctx, "o1", "BTC-USDX-PERP"); err != nil {
		t.Fatalf("cancel with a create in flight: %v", err)
	}
	select {
	case err := <-createDone:
		t.Fatalf("create finished first (%v); the test did not hold it", err)
	default:
	}
}

// TestBatchIsPassThrough: the batch goes out as one request carrying exactly
// the caller's orders, in order, however many there are.
func TestBatchIsPassThrough(t *testing.T) {
	reqs := make([]OrderRequest, 100)
	for i := range reqs {
		reqs[i] = OrderRequest{MarketId: "BTC-USDX-PERP", Side: Buy, OrderType: OrderTypeMarket,
			Quantity: mustDecimal(t, strconv.Itoa(i+1)), TimeInForce: IOC}
	}
	c, got := recorder(t, 201, `[{"outcome":"ok","order":`+orderJSON+`,"fills":[]},`+
		`{"outcome":"err","error":"insufficient_margin","message":"no"}]`)
	res, err := c.CreateOrders(context.Background(), reqs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got()) != 1 || got()[0].uri != "/orders/batch" {
		t.Fatalf("sent %+v, want one POST /orders/batch", got())
	}
	want, _ := json.Marshal(reqs)
	if !jsonEqual(t, got()[0].body, string(want)) {
		t.Error("batch body differs from the orders given")
	}
	if d, _ := res[0].Discriminator(); d != "ok" {
		t.Errorf("result 0 outcome %q", d)
	}
	if e, err := res[1].AsOrderResultErr(); err != nil || e.Error != "insufficient_margin" {
		t.Errorf("result 1 = %+v, %v", e, err)
	}
}

// TestNoPreviewCall: nothing in the SDK calls POST /orders/preview, which is
// billed as a trading action.
func TestNoPreviewCall(t *testing.T) {
	var preview atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/preview") {
			preview.Add(1)
		}
		io.WriteString(w, `{"order":`+orderJSON+`}`)
	})
	if _, err := c.CreateOrder(context.Background(), OrderRequest{}); err != nil {
		t.Fatal(err)
	}
	if n := preview.Load(); n != 0 {
		t.Fatalf("preview called %d times", n)
	}
}
