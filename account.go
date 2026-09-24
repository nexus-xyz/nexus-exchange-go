package nexus

import (
	"context"
	"net/http"

	"github.com/nexus-xyz/nexus-exchange-go/internal/models"
)

type (
	// AccountSummary is the account's balance, collateral, equity, available
	// margin and open positions, from [Account.Balance].
	AccountSummary = models.AccountSummary
	// CancelOnDisconnectStatus reports cancel-on-disconnect: Enabled is the
	// account's own opt-in, Active is whether it will actually fire (it also
	// needs the exchange-side switch), and GraceSecs is the reconnect window,
	// null when the feature is unavailable.
	CancelOnDisconnectStatus = models.CancelOnDisconnectStatus
)

// Account groups the operations on the /account singleton and its
// sub-resources. Get one from [Client.Account]; it holds no state of its own.
type Account struct {
	c *Client
}

// Account returns the account operations of c.
func (c *Client) Account() Account { return Account{c} }

// Balance returns the account summary (GET /account).
//
// It fails closed: when the engine's authoritative margin view is down the
// server answers 502, and Balance returns an error matching
// [ErrMarginUnavailable] and no summary. The SDK never substitutes a cached or
// computed figure. Treat that error as "unknown", never as an empty account.
func (a Account) Balance(ctx context.Context) (*AccountSummary, error) {
	var out AccountSummary
	if err := a.c.t.Get(ctx, "/account", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CancelOnDisconnect returns the account's cancel-on-disconnect status (GET
// /account/cancel-on-disconnect). See [Account.SetCancelOnDisconnect].
func (a Account) CancelOnDisconnect(ctx context.Context) (*CancelOnDisconnectStatus, error) {
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
// exposed. It is per account and off by default. It keys on the WebSocket
// connection, not on REST traffic: a bot that only uses REST is not covered.
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
