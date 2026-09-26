package nexus

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestParityOperationsOnTheWire pins the method, path, query and body of each
// operation added for parity with the sibling SDKs (ENG-17796), and one
// decoded field of each answer.
func TestParityOperationsOnTheWire(t *testing.T) {
	ctx := context.Background()
	amount := mustDecimal(t, "500")
	for _, tc := range []struct {
		name, resp, method, uri, body string
		call                          func(*Client) error
	}{
		{"claimCredit", `{"amount":"500","credited_today":"500","daily_limit":"1000"}`, "POST", "/account/credit", `{"amount":"500"}`,
			func(c *Client) error {
				r, err := c.Account().ClaimCredit(ctx, &amount)
				if err == nil && r.DailyLimit.String() != "1000" {
					t.Errorf("credit = %+v", r)
				}
				return err
			}},
		{"claimCredit all", `{"amount":"1000","credited_today":"1000","daily_limit":"1000"}`, "POST", "/account/credit", `{}`,
			func(c *Client) error { _, err := c.Account().ClaimCredit(ctx, nil); return err }},
		{"deposit", `{"balance":"110000.00"}`, "POST", "/account/deposit", `{"amount":"500"}`,
			func(c *Client) error {
				r, err := c.Account().Deposit(ctx, amount)
				if err == nil && r.Balance.String() != "110000.00" {
					t.Errorf("deposit = %+v", r)
				}
				return err
			}},
		{"addMargin", `{"market_id":"BTC-USDX-PERP","allocated_margin":"350.00","collateral":"9900.00"}`, "POST", "/account/margin",
			`{"market_id":"BTC-USDX-PERP","amount":"500","direction":"remove"}`,
			func(c *Client) error {
				r, err := c.Account().AddMargin(ctx, MarginRequest{MarketID: "BTC-USDX-PERP", Amount: amount, Direction: MarginRemove})
				if err == nil && r.AllocatedMargin.String() != "350.00" {
					t.Errorf("margin = %+v", r)
				}
				return err
			}},
		{"fetchEquityHistory", `[{"timestamp_ms":1,"equity":1012.34}]`, "GET", "/account/equity-history?cursor=c0&limit=10", "",
			func(c *Client) error {
				e, next, err := c.Account().FetchEquityHistory(ctx, Page{Limit: 10, Cursor: "c0"})
				if err == nil && (next != "next-1" || e[0].Equity.String() != "1012.34") {
					t.Errorf("equity = %+v, next %q", e, next)
				}
				return err
			}},
		{"fetchTradingFees", `{"tier":"base","schedule":"s","maker_fee_bps":2,"taker_fee_bps":5,"discounts":[]}`, "GET", "/account/fees", "",
			func(c *Client) error {
				f, err := c.Account().FetchTradingFees(ctx)
				if err == nil && f.TakerFeeBps != 5 {
					t.Errorf("fees = %+v", f)
				}
				return err
			}},
		{"fetchPortfolioHistory", `{"window":"week","cadence_ms":3600000,"points":[{"timestamp_ms":1,"equity":"10.5","pnl":"0.5","volume":"100"}]}`,
			"GET", "/account/portfolio-history?limit=24&window=week", "",
			func(c *Client) error {
				h, err := c.Account().FetchPortfolioHistory(ctx, "week", 24)
				if err == nil && (h.CadenceMs != 3600000 || h.Points[0].Pnl.String() != "0.5") {
					t.Errorf("portfolio = %+v", h)
				}
				return err
			}},
		{"fetchRateLimitStatus", `{"tier":"pro","limit":100,"remaining":99,"reset_at_ms":5}`, "GET", "/account/rate-limit", "",
			func(c *Client) error {
				r, err := c.Account().FetchRateLimitStatus(ctx)
				if err == nil && r.Remaining.MustGet() != 99 {
					t.Errorf("rate limit = %+v", r)
				}
				return err
			}},
		{"fetchAccountState", `{"summary":{"total_equity":"12.5"},"positions":[{"market_id":"BTC-USDX-PERP"}]}`, "GET", "/account/state", "",
			func(c *Client) error {
				s, err := c.Account().FetchAccountState(ctx)
				if err == nil && (s.Summary.TotalEquity.String() != "12.5" || len(s.Positions) != 1) {
					t.Errorf("state = %+v", s)
				}
				return err
			}},
		{"fetchAccountSummary", `{"withdrawable":"7.25"}`, "GET", "/account/summary", "",
			func(c *Client) error {
				s, err := c.Account().FetchAccountSummary(ctx)
				if err == nil && s.Withdrawable.String() != "7.25" {
					t.Errorf("summary = %+v", s)
				}
				return err
			}},
		{"fetchAdlHistory", `[]`, "GET", "/account/0xabc/adl-history?limit=5", "",
			func(c *Client) error { _, err := c.FetchAdlHistory(ctx, "0xabc", 5); return err }},
		{"fetchDeposits", `[{"id":7,"kind":"deposit","amount":"10"}]`, "GET", "/deposits?limit=3", "",
			func(c *Client) error {
				d, err := c.FetchDeposits(ctx, 3)
				if err == nil && *d[0].Id != 7 {
					t.Errorf("deposits = %+v", d)
				}
				return err
			}},
		{"createDeposit", `{"balance":"10"}`, "POST", "/deposits", `{"amount":"500"}`,
			func(c *Client) error { _, err := c.CreateDeposit(ctx, DepositRequest{Amount: amount}); return err }},
		{"fetchWithdrawals", `[{"id":"w1","amount":"1.5","timestamp":1,"status":"settled"}]`, "GET", "/withdrawals", "",
			func(c *Client) error {
				w, err := c.FetchWithdrawals(ctx, 0)
				if err == nil && w[0].Amount.String() != "1.5" {
					t.Errorf("withdrawals = %+v", w)
				}
				return err
			}},
		{"claimFaucet", `{"amount":"100","available_at_ms":9}`, "POST", "/faucet", "",
			func(c *Client) error { _, err := c.ClaimFaucet(ctx); return err }},
		{"fetchBridgeAssets", `{"chains":[{"chain":"ethereum","deposit_assets":[],"withdraw_assets":[]}]}`, "GET", "/bridge/assets", "",
			func(c *Client) error {
				a, err := c.FetchBridgeAssets(ctx)
				if err == nil && a.Chains[0].Chain != "ethereum" {
					t.Errorf("bridge assets = %+v", a)
				}
				return err
			}},
		{"fetchBridgeDeposits", `[]`, "GET", "/bridge/deposits?asset=USDC&limit=2&status=credited", "",
			func(c *Client) error {
				_, err := c.FetchBridgeDeposits(ctx, BridgeDepositsParams{Limit: 2, Asset: "USDC", Status: "credited"})
				return err
			}},
		{"fetchBridgeDeposit", `{"id":"d/1","amount":"5"}`, "GET", "/bridge/deposits/d%2F1", "",
			func(c *Client) error { _, err := c.FetchBridgeDeposit(ctx, "d/1"); return err }},
		{"previewOrder", `{"accepted":false,"reject_reason":"insufficient_margin"}`, "POST", "/orders/preview", `{"market_id":"BTC-USDX-PERP","side":"Buy","order_type":"Market","quantity":"500","time_in_force":"IOC"}`,
			func(c *Client) error {
				p, err := c.PreviewOrder(ctx, OrderRequest{MarketId: "BTC-USDX-PERP", Side: Buy, OrderType: OrderTypeMarket, Quantity: amount, TimeInForce: IOC})
				if err == nil && (*p.Accepted || p.RejectReason.MustGet() != "insufficient_margin") {
					t.Errorf("preview = %+v", p)
				}
				return err
			}},
		{"fetchPositionsHistory", `[{"market_id":"BTC-USDX-PERP","realized_pnl":"-3.5"}]`, "GET", "/positions/closed?limit=20", "",
			func(c *Client) error {
				p, next, err := c.FetchPositionsHistory(ctx, Page{Limit: 20})
				if err == nil && (next != "next-1" || p[0].RealizedPnl.String() != "-3.5") {
					t.Errorf("closed = %+v, next %q", p, next)
				}
				return err
			}},
		{"fetchTiers", `[{"address":"0xabc","tier":"MarketMaker"}]`, "GET", "/admin/tiers", "",
			func(c *Client) error {
				tiers, err := c.FetchTiers(ctx)
				if err == nil && tiers[0].Tier != "MarketMaker" {
					t.Errorf("tiers = %+v", tiers)
				}
				return err
			}},
		{"setTier", `{"address":"0xabc","tier":"MarketMaker"}`, "PUT", "/admin/tiers", `{"address":"0xabc","tier":"MarketMaker"}`,
			func(c *Client) error { _, err := c.SetTier(ctx, "0xabc", "MarketMaker"); return err }},
		{"deleteTier", ``, "DELETE", "/admin/tiers/0xabc", "",
			func(c *Client) error { return c.DeleteTier(ctx, "0xabc") }},
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

// TestAdminSecret: the admin credential is a bearer header, never printed,
// acts for no account, and is one credential among the rest.
func TestAdminSecret(t *testing.T) {
	var auth string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Write([]byte(`[]`))
	})
	cfg := config{}
	WithAdminSecret("s3cret")(&cfg)
	if err := cfg.creds[0](c); err != nil {
		t.Fatal(err)
	}
	if _, err := c.FetchTiers(context.Background()); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer s3cret" {
		t.Errorf("Authorization = %q", auth)
	}
	if s := fmt.Sprintf("%+v %#v", c.t.Signer, c.t.Signer); strings.Contains(s, "s3cret") {
		t.Errorf("printed the secret: %s", s)
	}
	if _, err := NewClient(Testnet, WithAdminSecret("")); err == nil {
		t.Error("empty secret accepted")
	}
	secret, _ := NewAPISecret(strings.Repeat("00", 32))
	if _, err := NewClient(Testnet, WithAdminSecret("s"), WithHMACAuth("k", secret)); err == nil {
		t.Error("two credentials accepted")
	}
	a, _ := NewClient(Testnet, WithAdminSecret("s"))
	if _, err := a.AccountAddress(context.Background()); err != ErrNoCredential {
		t.Errorf("AccountAddress = %v, want ErrNoCredential", err)
	}
}
