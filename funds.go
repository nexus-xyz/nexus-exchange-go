package nexus

import (
	"context"
	"net/http"
	"net/url"

	"github.com/nexus-xyz/nexus-exchange-go/internal/models"
)

// Funds types, as the pinned spec defines them.
type (
	// FundsEntry is one deposit, withdrawal or faucet entry of the account's
	// ledger, from [Client.FetchDeposits].
	FundsEntry = models.FundsEntry
	// DepositRequest is the body of [Client.CreateDeposit]. Asset defaults to
	// USDX when nil.
	DepositRequest = models.DepositRequest
	// DepositResponse is the engine's acknowledgement of
	// [Client.CreateDeposit], with the authoritative balance after it.
	DepositResponse = models.DepositResponse
	// Withdrawal is one withdrawal of the account, from
	// [Client.FetchWithdrawals].
	Withdrawal = models.Withdrawal
	// FaucetResponse is what [Client.ClaimFaucet] credited, and when the
	// faucet can be claimed again.
	FaucetResponse = models.FaucetResponse
	// BridgeAssetsResponse lists the bridge's chains and the assets each one
	// deposits and withdraws, from [Client.FetchBridgeAssets].
	BridgeAssetsResponse = models.BridgeAssetsResponse
	// BridgeDeposit is one cross-chain deposit the watcher tracks, from
	// [Client.FetchBridgeDeposits] and [Client.FetchBridgeDeposit].
	BridgeDeposit = models.BridgeDeposit
)

// FetchDeposits returns up to limit of the account's deposit ledger entries
// (GET /deposits). Zero limit leaves it to the server.
func (c *Client) FetchDeposits(ctx context.Context, limit int) ([]FundsEntry, error) {
	return getJSON[[]FundsEntry](ctx, c, "/deposits", limitQuery(limit))
}

// CreateDeposit records a deposit of req.Amount (POST /deposits). On a
// real-funds network this is real collateral. It is sent once and never
// retried.
func (c *Client) CreateDeposit(ctx context.Context, req DepositRequest) (*DepositResponse, error) {
	var out DepositResponse
	if err := c.t.Send(ctx, http.MethodPost, "/deposits", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// FetchWithdrawals returns up to limit of the account's withdrawals (GET
// /withdrawals). Zero limit leaves it to the server. Reading withdrawal
// history is allowed to an agent key; withdrawing is not.
func (c *Client) FetchWithdrawals(ctx context.Context, limit int) ([]Withdrawal, error) {
	return getJSON[[]Withdrawal](ctx, c, "/withdrawals", limitQuery(limit))
}

// ClaimFaucet claims testnet USDX from the faucet (POST /faucet). It is play
// funds only, sent once and never retried; a claim before AvailableAtMs of the
// last one is refused.
func (c *Client) ClaimFaucet(ctx context.Context) (*FaucetResponse, error) {
	var out FaucetResponse
	if err := c.t.Send(ctx, http.MethodPost, "/faucet", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// The bridge reads. The pinned spec ([APIVersion]) declares them only on the
// /api/v1 dual mount (GET /api/v1/bridge/assets and so on). This SDK sends
// every route on the /v1 edge mount instead (see [Network]), so they go out as
// /v1/bridge/..., the same indexer route.

// BridgeDepositsParams filters [Client.FetchBridgeDeposits]. Zero fields are
// not sent.
type BridgeDepositsParams struct {
	// Limit is the most entries one response holds; zero leaves it to the
	// server.
	Limit int
	// Chain, Asset ("USDC" or "USDX") and Status ("detected", "confirming",
	// "credited" or "failed") narrow the list.
	Chain, Asset, Status string
}

// FetchBridgeAssets returns the bridge's supported chains and assets (GET
// /api/v1/bridge/assets in the pinned spec).
func (c *Client) FetchBridgeAssets(ctx context.Context) (*BridgeAssetsResponse, error) {
	return getJSON[*BridgeAssetsResponse](ctx, c, "/bridge/assets", nil)
}

// FetchBridgeDeposits returns the account's cross-chain deposits (GET
// /api/v1/bridge/deposits in the pinned spec).
func (c *Client) FetchBridgeDeposits(ctx context.Context, p BridgeDepositsParams) ([]BridgeDeposit, error) {
	q := limitQuery(p.Limit)
	for k, v := range map[string]string{"chain": p.Chain, "asset": p.Asset, "status": p.Status} {
		if v != "" {
			q.Set(k, v)
		}
	}
	return getJSON[[]BridgeDeposit](ctx, c, "/bridge/deposits", q)
}

// FetchBridgeDeposit returns one cross-chain deposit (GET
// /api/v1/bridge/deposits/{id} in the pinned spec). A deposit that is not
// yours is reported as [ErrNotFound].
func (c *Client) FetchBridgeDeposit(ctx context.Context, id string) (*BridgeDeposit, error) {
	return getJSON[*BridgeDeposit](ctx, c, "/bridge/deposits/"+url.PathEscape(id), nil)
}
