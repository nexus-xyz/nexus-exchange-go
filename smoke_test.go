package nexus

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestTestnetSmoke runs the read-only authenticated operations against
// testnet (play funds). It needs an API key in NEXUS_TESTNET_KEY_ID and
// NEXUS_TESTNET_KEY_SECRET (the secret's hex text) and skips without one. It
// places, edits and cancels nothing.
//
//	NEXUS_TESTNET_KEY_ID=... NEXUS_TESTNET_KEY_SECRET=... go test -run TestTestnetSmoke -v .
func TestTestnetSmoke(t *testing.T) {
	keyID, secretHex := os.Getenv("NEXUS_TESTNET_KEY_ID"), os.Getenv("NEXUS_TESTNET_KEY_SECRET")
	if keyID == "" || secretHex == "" {
		t.Skip("NEXUS_TESTNET_KEY_ID / NEXUS_TESTNET_KEY_SECRET not set")
	}
	secret, err := NewAPISecret(secretHex)
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(Testnet, WithHMACAuth(keyID, secret))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	if a, err := c.Account().Balance(ctx); err != nil {
		t.Errorf("Balance: %v", err)
	} else {
		t.Logf("Balance: equity %v", a.Equity)
	}
	if p, err := c.Positions(ctx); err != nil {
		t.Errorf("Positions: %v", err)
	} else {
		t.Logf("Positions: %d", len(p))
	}
	if o, err := c.OpenOrders(ctx); err != nil {
		t.Errorf("OpenOrders: %v", err)
	} else {
		t.Logf("OpenOrders: %d", len(o))
	}
	if h, _, err := c.OrderHistory(ctx, Page{Limit: 5}); err != nil {
		t.Errorf("OrderHistory: %v", err)
	} else {
		t.Logf("OrderHistory: %d", len(h))
	}
	if f, _, err := c.Fills(ctx, Page{Limit: 5}); err != nil {
		t.Errorf("Fills: %v", err)
	} else {
		t.Logf("Fills: %d", len(f))
	}
	if s, err := c.Account().CancelOnDisconnect(ctx); err != nil {
		t.Errorf("CancelOnDisconnect: %v", err)
	} else {
		t.Logf("CancelOnDisconnect: enabled %v active %v", s.Enabled, s.Active)
	}
}
