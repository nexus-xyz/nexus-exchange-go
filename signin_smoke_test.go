package nexus

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// TestTestnetSignIn runs the wallet postures end to end against testnet with
// a throwaway wallet, so it needs no funds and no secret: sign-in, a session
// read, an API key minted by the session, agent registration, an agent read,
// and the local R2.18 refusal. It cleans up the agent and the key.
//
// It writes to testnet (a key and an agent under a fresh wallet), so it runs
// only when asked:
//
//	NEXUS_TESTNET_SIGNIN=1 go test -run TestTestnetSignIn -v .
func TestTestnetSignIn(t *testing.T) {
	if os.Getenv("NEXUS_TESTNET_SIGNIN") == "" {
		t.Skip("NEXUS_TESTNET_SIGNIN not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	wallet, err := GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	pub, err := NewClient(Testnet)
	if err != nil {
		t.Fatal(err)
	}

	sess, err := pub.SignIn(ctx, wallet)
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	if sess.Address() != wallet.Address() {
		t.Fatalf("session address %s, want %s", sess.Address(), wallet.Address())
	}
	sc, _ := NewClient(Testnet, WithSession(sess))
	if _, err := sc.APIKeys(ctx); err != nil {
		t.Fatalf("APIKeys with session: %v", err)
	}
	key, err := sc.CreateAPIKey(ctx)
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	defer func() {
		if err := sc.DeleteAPIKey(context.Background(), key.KeyID); err != nil {
			t.Errorf("DeleteAPIKey: %v", err)
		}
	}()

	hc, _ := NewClient(Testnet, WithHMACAuth(key.KeyID, key.Secret))
	// Resolved through GET /account/deposit-target, which the indexer answers
	// alone, so this holds even while the engine is down.
	if got, err := hc.AccountAddress(ctx); err != nil {
		t.Errorf("AccountAddress with HMAC: %v", err)
	} else if got != wallet.Address() {
		t.Errorf("AccountAddress with HMAC = %s, want %s", got, wallet.Address())
	}

	agentKey, _ := GeneratePrivateKey()
	agent, err := pub.RegisterAgent(ctx, wallet, agentKey, RegisterAgentOptions{Label: "go-sdk-smoke"})
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	defer func() {
		if err := hc.RevokeAgent(context.Background(), agent.Address()); err != nil {
			t.Errorf("RevokeAgent: %v", err)
		}
	}()
	if list, err := hc.Agents(ctx); err != nil || len(list) != 1 {
		t.Errorf("Agents = %d, %v; want the one just registered", len(list), err)
	}
	ac, _ := NewClient(Testnet, WithAgent(agent))
	if _, err := ac.OpenOrders(ctx); err != nil {
		t.Errorf("OpenOrders with agent: %v", err)
	}
	if err := ac.t.Send(ctx, "POST", "/withdrawals", map[string]string{"amount": "1"}, nil); !errors.Is(err, ErrAgentCannotWithdraw) {
		t.Errorf("agent withdrawal = %v, want ErrAgentCannotWithdraw", err)
	}
}
