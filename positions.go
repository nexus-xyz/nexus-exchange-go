package nexus

import (
	"context"

	"github.com/nexus-xyz/nexus-exchange-go/internal/models"
)

// Position is an open position. A risk field the server cannot derive is null,
// and its companion *Error field says why; it is never a made-up number.
type Position = models.Position

// Positions returns the account's open positions (GET /positions).
//
// It fails closed like [Account.Balance]: an outage of the authoritative
// margin view is an error matching [ErrMarginUnavailable] and no positions,
// never an empty list.
func (c *Client) Positions(ctx context.Context) ([]Position, error) {
	var out []Position
	if err := c.t.Get(ctx, "/positions", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}
