package nexus

import (
	"context"
	"errors"
	"testing"
)

// TestMarginUnavailableFailsClosed: a 502 authoritative_margin_unavailable on
// balance or positions is ErrMarginUnavailable with no value, never a
// substitute.
func TestMarginUnavailableFailsClosed(t *testing.T) {
	ctx := context.Background()
	body := `{"code":"authoritative_margin_unavailable","message":"engine margin view unavailable"}`
	t.Run("balance", func(t *testing.T) {
		c, _ := recorder(t, 502, body)
		a, err := c.Account().FetchBalance(ctx)
		if !errors.Is(err, ErrMarginUnavailable) || a != nil {
			t.Fatalf("Balance = %v, %v; want nil, ErrMarginUnavailable", a, err)
		}
	})
	t.Run("positions", func(t *testing.T) {
		c, _ := recorder(t, 502, body)
		p, err := c.FetchPositions(ctx)
		if !errors.Is(err, ErrMarginUnavailable) || p != nil {
			t.Fatalf("Positions = %v, %v; want nil, ErrMarginUnavailable", p, err)
		}
	})
}
