package nexus

import (
	"context"
	"net/http"
	"net/url"

	"github.com/nexus-xyz/nexus-exchange-go/internal/models"
)

type (
	// AccountSummary is the account's balance, collateral, equity, available
	// margin and open positions, from [Account.FetchBalance].
	AccountSummary = models.AccountSummary
	// CancelOnDisconnectStatus reports cancel-on-disconnect: Enabled is the
	// account's own opt-in, Active is whether it will actually fire (it also
	// needs the exchange-side switch), and GraceSecs is the reconnect window,
	// null when the feature is unavailable.
	CancelOnDisconnectStatus = models.CancelOnDisconnectStatus
	// CreditResponse is what [Account.ClaimCredit] credited, and the day's
	// running total against its limit.
	CreditResponse = models.CreditResponse
	// EquityPoint is one equity sample from [Account.FetchEquityHistory].
	// Equity is a JSON number in the pinned spec, kept as its served digits.
	EquityPoint = models.EquityPoint
	// AccountFees is the account's effective fee schedule, from
	// [Account.FetchTradingFees].
	AccountFees = models.AccountFees
	// RateLimitStatus is the account's rate-limit tier and remaining budget,
	// from [Account.FetchRateLimitStatus].
	RateLimitStatus = models.RateLimitStatus
	// AccountState is the portfolio summary and every open position from one
	// read, from [Account.FetchAccountState].
	AccountState = models.AccountState
	// AccountPortfolioSummary is the account's aggregate equity, PnL, volume
	// and open counts, from [Account.FetchAccountSummary].
	AccountPortfolioSummary = models.AccountPortfolioSummary
)

// DepositResult is what [Account.Deposit] returns: the authoritative balance
// after the deposit. The pinned spec ([APIVersion]) publishes only an example
// for this response, so the type is written here from it.
type DepositResult struct {
	Balance Decimal `json:"balance"`
}

// MarginDirection says whether [Account.AddMargin] adds isolated margin to a
// position or removes it.
type MarginDirection string

const (
	MarginAdd    MarginDirection = "add"
	MarginRemove MarginDirection = "remove"
)

// MarginRequest is the body of [Account.AddMargin]. The pinned spec
// ([APIVersion]) publishes only an example for it, so the type is written
// here from it.
type MarginRequest struct {
	MarketID  string          `json:"market_id"`
	Amount    Decimal         `json:"amount"`
	Direction MarginDirection `json:"direction"`
}

// MarginResult is the position's isolated margin and the account's
// collateral after [Account.AddMargin]. Written from the pinned spec's
// example, like [MarginRequest].
type MarginResult struct {
	MarketID        string  `json:"market_id"`
	AllocatedMargin Decimal `json:"allocated_margin"`
	Collateral      Decimal `json:"collateral"`
}

// PortfolioHistory is the account's equity, cumulative PnL and cumulative
// volume over a window, oldest first, from [Account.FetchPortfolioHistory].
// It is written here from the pinned spec's PortfolioHistory schema, which the
// model generator cannot emit (its PortfolioWindow is also a parameter name).
type PortfolioHistory struct {
	// Window is the window served: "day", "week", "month" or "all".
	Window string `json:"window"`
	// CadenceMs is the interval between adjacent points.
	CadenceMs int64            `json:"cadence_ms"`
	Points    []PortfolioPoint `json:"points"`
}

// PortfolioPoint is one sample of a [PortfolioHistory].
type PortfolioPoint struct {
	TimestampMs int64   `json:"timestamp_ms"`
	Equity      Decimal `json:"equity"`
	Pnl         Decimal `json:"pnl"`
	Volume      Decimal `json:"volume"`
}

// Account groups the operations on the /account singleton and its
// sub-resources. Get one from [Client.Account]; it holds no state of its own.
type Account struct {
	c *Client
}

// Account returns the account operations of c.
func (c *Client) Account() Account { return Account{c} }

// FetchBalance returns the account summary (GET /account).
//
// It fails closed: when the engine's authoritative margin view is down the
// server answers 502, and FetchBalance returns an error matching
// [ErrMarginUnavailable] and no summary. The SDK never substitutes a cached or
// computed figure. Treat that error as "unknown", never as an empty account.
func (a Account) FetchBalance(ctx context.Context) (*AccountSummary, error) {
	var out AccountSummary
	if err := a.c.t.Get(ctx, "/account", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// FetchCancelOnDisconnect returns the account's cancel-on-disconnect status (GET
// /account/cancel-on-disconnect). See [Account.SetCancelOnDisconnect].
func (a Account) FetchCancelOnDisconnect(ctx context.Context) (*CancelOnDisconnectStatus, error) {
	var out CancelOnDisconnectStatus
	if err := a.c.t.Get(ctx, "/account/cancel-on-disconnect", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SetCancelOnDisconnect turns cancel-on-disconnect on or off (PUT
// /account/cancel-on-disconnect) and returns the resulting status.
//
// Cancel-on-disconnect is the safety net for a bot. Once enabled, when the
// account's last authenticated WebSocket connection drops and does not
// reconnect within the grace window, the exchange cancels every resting order
// on the account, so a crashed or partitioned process cannot leave orders
// exposed. It is per account and off by default. It keys on the account
// socket (/ws), not on REST traffic or the public [MarketStream]: it protects
// only a bot that holds a [Subscription] open (see [Client.Subscribe]), and a
// bot that only uses REST is not covered.
// Check Active on the result, not just Enabled: the exchange-side switch must
// also be on for it to fire.
//
// Like every mutation, it is sent once and never retried.
func (a Account) SetCancelOnDisconnect(ctx context.Context, enabled bool) (*CancelOnDisconnectStatus, error) {
	var out CancelOnDisconnectStatus
	body := models.SetCancelOnDisconnectRequest{Enabled: enabled}
	if err := a.c.t.Send(ctx, http.MethodPut, "/account/cancel-on-disconnect", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ClaimCredit claims synthetic USDX from the testnet faucet allowance (POST
// /account/credit). A nil amount claims the whole remaining daily allowance.
// It is play funds only; the call that moves real collateral is
// [Account.Deposit]. Like every mutation, it is sent once and never retried.
func (a Account) ClaimCredit(ctx context.Context, amount *Decimal) (*CreditResponse, error) {
	var out CreditResponse
	if err := a.c.t.Send(ctx, http.MethodPost, "/account/credit", models.CreditRequest{Amount: amount}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Deposit deposits USDX collateral (POST /account/deposit) and returns the
// authoritative balance after it. On a real-funds network this moves real
// collateral; to fund a testnet account use [Account.ClaimCredit]. It is sent
// once and never retried.
func (a Account) Deposit(ctx context.Context, amount Decimal) (*DepositResult, error) {
	var out DepositResult
	body := struct {
		Amount Decimal `json:"amount"`
	}{amount}
	if err := a.c.t.Send(ctx, http.MethodPost, "/account/deposit", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AddMargin adds isolated margin to an open position, or removes it when
// req.Direction is [MarginRemove] (POST /account/margin). The server refuses a
// cross-margined position, a market with no open position, and a removal
// below the withdrawal floor. It is sent once and never retried.
func (a Account) AddMargin(ctx context.Context, req MarginRequest) (*MarginResult, error) {
	var out MarginResult
	if err := a.c.t.Send(ctx, http.MethodPost, "/account/margin", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// FetchEquityHistory returns one page of equity samples at 5 second cadence,
// oldest first (GET /account/equity-history), and the cursor for the next
// page, empty on the last. For a long window, [Account.FetchPortfolioHistory]
// is the downsampled series.
func (a Account) FetchEquityHistory(ctx context.Context, p Page) ([]EquityPoint, string, error) {
	var out []EquityPoint
	next, err := a.c.t.GetPage(ctx, "/account/equity-history", p.query(), &out)
	if err != nil {
		return nil, "", err
	}
	return out, next, nil
}

// FetchTradingFees returns the account's effective maker and taker fees (GET
// /account/fees).
func (a Account) FetchTradingFees(ctx context.Context) (*AccountFees, error) {
	return getJSON[*AccountFees](ctx, a.c, "/account/fees", nil)
}

// FetchPortfolioHistory returns the account's portfolio series over window
// ("day", "week", "month" or "all"; empty leaves the server's "day"), at most
// limit points (zero leaves it to the server) (GET /account/portfolio-history).
func (a Account) FetchPortfolioHistory(ctx context.Context, window string, limit int) (*PortfolioHistory, error) {
	q := limitQuery(limit)
	if window != "" {
		q.Set("window", window)
	}
	return getJSON[*PortfolioHistory](ctx, a.c, "/account/portfolio-history", q)
}

// FetchRateLimitStatus returns the account's rate-limit tier, limit and
// remaining budget (GET /account/rate-limit).
func (a Account) FetchRateLimitStatus(ctx context.Context) (*RateLimitStatus, error) {
	return getJSON[*RateLimitStatus](ctx, a.c, "/account/rate-limit", nil)
}

// FetchAccountState returns the portfolio summary and every open position,
// built from one server-side read so the two always agree (GET
// /account/state).
func (a Account) FetchAccountState(ctx context.Context) (*AccountState, error) {
	return getJSON[*AccountState](ctx, a.c, "/account/state", nil)
}

// FetchAccountSummary returns the account's aggregate equity, PnL, volume and
// open counts (GET /account/summary).
func (a Account) FetchAccountSummary(ctx context.Context) (*AccountPortfolioSummary, error) {
	return getJSON[*AccountPortfolioSummary](ctx, a.c, "/account/summary", nil)
}

// FetchAdlHistory returns up to limit auto-deleveraging settlements that
// touched the account at address, newest first (GET
// /account/{address}/adl-history). Zero limit leaves it to the server. The
// server requires a credential for this read.
func (c *Client) FetchAdlHistory(ctx context.Context, address string, limit int) ([]AdlEventRecord, error) {
	return getJSON[[]AdlEventRecord](ctx, c, "/account/"+url.PathEscape(address)+"/adl-history", limitQuery(limit))
}
