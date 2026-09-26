package nexus

import (
	"context"

	"github.com/nexus-xyz/nexus-exchange-go/internal/models"
)

// Position is an open position. A risk field the server cannot derive is null,
// and its companion *Error field says why; it is never a made-up number.
type Position = models.Position

// ClosedPosition is a position that has closed, from
// [Client.FetchPositionsHistory].
type ClosedPosition = models.ClosedPosition

// FetchPositions returns the account's open positions (GET /positions).
//
// It fails closed like [Account.FetchBalance]: an outage of the authoritative
// margin view is an error matching [ErrMarginUnavailable] and no positions,
// never an empty list.
func (c *Client) FetchPositions(ctx context.Context) ([]Position, error) {
	var out []Position
	if err := c.t.Get(ctx, "/positions", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// FetchPositionsHistory returns one page of the account's closed positions,
// newest first (GET /positions/closed), and the cursor for the next page,
// empty on the last.
func (c *Client) FetchPositionsHistory(ctx context.Context, p Page) ([]ClosedPosition, string, error) {
	var out []ClosedPosition
	next, err := c.t.GetPage(ctx, "/positions/closed", p.query(), &out)
	if err != nil {
		return nil, "", err
	}
	return out, next, nil
}
