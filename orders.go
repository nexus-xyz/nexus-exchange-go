package nexus

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/nexus-xyz/nexus-exchange-go/internal/models"
)

// Order types, as the pinned spec defines them. Fields that the spec marks
// nullable are [github.com/oapi-codegen/nullable.Nullable] values, which keep
// an explicit null apart from an absent key.
type (
	// OrderRequest is the body of [Client.CreateOrder] and one entry of
	// [Client.CreateOrders]. It is snake_case on the wire (market_id,
	// quantity, time_in_force).
	OrderRequest = models.OrderRequest
	// OrderResponse is what [Client.CreateOrder] returns: the placed order and
	// any fills it took immediately. At the pinned spec ([APIVersion]) it is
	// the snake_case {order, fills} shape.
	OrderResponse = models.OrderResponse
	// OrderResult is one entry of a [Client.CreateOrders] response.
	// Branch on Discriminator ("ok" or "err"), then read it with
	// AsOrderResultOk or AsOrderResultErr.
	OrderResult    = models.OrderResult
	OrderResultOk  = models.OrderResultOk
	OrderResultErr = models.OrderResultErr
	// AmendOrderRequest is the body of [Client.EditOrder]: a new price, a new
	// size, or both.
	AmendOrderRequest = models.AmendOrderRequest
	// Order is an order as GET /orders serves it.
	Order = models.Order
	// OrderHistoryEntry is a terminal order from [Client.FetchOrders].
	OrderHistoryEntry = models.OrderHistoryEntry
	// Fill is one execution from [Client.FetchMyTrades].
	Fill = models.Fill

	OrderType   = models.OrderRequestOrderType
	OrderSide   = models.OrderRequestSide
	TimeInForce = models.OrderRequestTimeInForce
	// STP is the self-trade prevention mode an order is placed with.
	STP = models.OrderRequestStp
)

// Values for the enums of an [OrderRequest]. Responses are open enums: a
// value not listed here decodes without error.
const (
	OrderTypeLimit            = models.OrderRequestOrderTypeLimit
	OrderTypeMarket           = models.OrderRequestOrderTypeMarket
	OrderTypeStopLimit        = models.OrderRequestOrderTypeStopLimit
	OrderTypeStopMarket       = models.OrderRequestOrderTypeStopMarket
	OrderTypeTakeProfitLimit  = models.OrderRequestOrderTypeTakeProfitLimit
	OrderTypeTakeProfitMarket = models.OrderRequestOrderTypeTakeProfitMarket
	OrderTypeTrailingLimit    = models.OrderRequestOrderTypeTrailingLimit
	OrderTypeTrailingStop     = models.OrderRequestOrderTypeTrailingStop

	Buy  = models.OrderRequestSideBuy
	Sell = models.OrderRequestSideSell

	GTC      = models.OrderRequestTimeInForceGTC
	IOC      = models.OrderRequestTimeInForceIOC
	FOK      = models.OrderRequestTimeInForceFOK
	PostOnly = models.OrderRequestTimeInForcePostOnly

	STPCancelNewest       = models.OrderRequestStpCancelNewest
	STPCancelOldest       = models.OrderRequestStpCancelOldest
	STPDecrementAndCancel = models.OrderRequestStpDecrementAndCancel
)

// Page selects one page of a cursor-paginated list. The zero value asks for
// the first page at the server's default size.
type Page struct {
	// Limit is the page size; zero leaves it to the server.
	Limit int
	// Cursor is the next cursor a previous call returned; empty for the first
	// page. It is opaque: pass it back unchanged.
	Cursor string
}

func (p Page) query() url.Values {
	q := url.Values{}
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	if p.Cursor != "" {
		q.Set("cursor", p.Cursor)
	}
	return q
}

// CreateOrder places one order (POST /orders).
//
// It is sent exactly once and never retried, like every method that changes
// state. On any failure, a timeout or a 5xx included, the order may or may not
// have been accepted: read it back with FetchOpenOrders or FetchOrder before
// resubmitting.
//
// POST /orders/preview is billed as a trading action: it spends the same
// order budget as this method, so previewing before every order halves the
// effective placement rate, and nothing in the name suggests that. This SDK
// has no preview method yet, and nothing in it calls that route (a test
// keeps it that way); if you call it yourself, budget for it as an order.
func (c *Client) CreateOrder(ctx context.Context, req OrderRequest) (*OrderResponse, error) {
	var out OrderResponse
	if err := c.t.Send(ctx, http.MethodPost, "/orders", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateOrders places reqs in one request (POST /orders/batch) and
// returns one result per order, in request order. The batch is sequential and
// not atomic: a rejected order does not abort the rest, and an early order
// can use margin a later one needed. Check every result.
//
// The request costs 1 + floor(len(reqs)/40) units of the trading budget, so up
// to 39 orders cost the same as one; the client paces it on that weight, read
// from the pinned spec. The batch is sent exactly as given: the SDK does not
// split it into chunks, and it is never retried.
func (c *Client) CreateOrders(ctx context.Context, reqs []OrderRequest) ([]OrderResult, error) {
	var out []OrderResult
	if err := c.t.Send(ctx, http.MethodPost, "/orders/batch", reqs, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// EditOrder amends a resting order's price, size or both in one atomic
// cancel-replace (PATCH /orders/{id}). It is never retried.
func (c *Client) EditOrder(ctx context.Context, orderID, marketID string, req AmendOrderRequest) (*Order, error) {
	var out Order
	if err := c.t.Send(ctx, http.MethodPatch, orderPath(orderID, marketID), req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CancelOrder cancels one order (DELETE /orders/{id}) and returns it. It is
// never retried.
//
// A cancel is never delayed. The client paces reads and submissions on their
// own budgets, but has no budget for cancels at all, so a cancel goes out the
// moment it is called, however many submissions are in flight or waiting. The
// server keeps cancels on a budget of their own so risk can always be reduced,
// and the SDK does not undo that; if that budget is spent, the 429 (Bucket
// "cancel") comes back at once rather than being waited out.
// An *http.Client given with [WithHTTPClient] that caps connections per host
// can still serialize requests; do not cap it below the concurrency you need.
//
// The pinned spec ([APIVersion]) documents no body for this response; the
// order decoded is the Order shape the server serves.
func (c *Client) CancelOrder(ctx context.Context, orderID, marketID string) (*Order, error) {
	var out Order
	if err := c.t.Send(ctx, http.MethodDelete, orderPath(orderID, marketID), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CancelAllOrders cancels every resting order (DELETE /orders), or only those
// in marketID when it is not empty, and returns the orders it cancelled. A
// short list does not prove nothing rests: check FetchOpenOrders. It is sent at
// once and never retried.
func (c *Client) CancelAllOrders(ctx context.Context, marketID string) ([]Order, error) {
	path := "/orders"
	if marketID != "" {
		path += "?" + url.Values{"market_id": {marketID}}.Encode()
	}
	var out []Order
	if err := c.t.Send(ctx, http.MethodDelete, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// FetchOrder returns one order (GET /orders/{id}). An order that is not yours is
// reported as [ErrNotFound].
func (c *Client) FetchOrder(ctx context.Context, orderID, marketID string) (*Order, error) {
	var out Order
	if err := c.t.Get(ctx, "/orders/"+url.PathEscape(orderID), url.Values{"market_id": {marketID}}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// FetchOpenOrders returns the account's resting orders (GET /orders).
func (c *Client) FetchOpenOrders(ctx context.Context) ([]Order, error) {
	var out []Order
	if err := c.t.Get(ctx, "/orders", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// FetchOrders returns one page of terminal orders (filled, cancelled,
// rejected, expired), newest first (GET /orders/history), and the cursor for
// the next page, empty on the last.
func (c *Client) FetchOrders(ctx context.Context, p Page) ([]OrderHistoryEntry, string, error) {
	var out []OrderHistoryEntry
	next, err := c.t.GetPage(ctx, "/orders/history", p.query(), &out)
	if err != nil {
		return nil, "", err
	}
	return out, next, nil
}

// FetchMyTrades returns one page of the account's fills, newest first (GET /fills),
// and the cursor for the next page, empty on the last.
func (c *Client) FetchMyTrades(ctx context.Context, p Page) ([]Fill, string, error) {
	var out []Fill
	next, err := c.t.GetPage(ctx, "/fills", p.query(), &out)
	if err != nil {
		return nil, "", err
	}
	return out, next, nil
}

func orderPath(orderID, marketID string) string {
	return "/orders/" + url.PathEscape(orderID) + "?" + url.Values{"market_id": {marketID}}.Encode()
}
