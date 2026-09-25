package nexus

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nexus-xyz/nexus-exchange-go/internal/models"
	"github.com/nexus-xyz/nexus-exchange-go/internal/signing"
)

// fakeVenue verifies every credential the way the server does: it recovers
// the EIP-191 sign-in and EIP-712 registration signers, checks each agent
// signature against x-agent, and maps each credential to its owner.
type fakeVenue struct {
	t        *testing.T
	hmacKey  string // X-Api-Key accepted, owned by hmacOwner
	hmacOwnr string

	mu       sync.Mutex
	sessions map[string]string // token -> owner
	agents   map[string]string // agent -> owner
	logins   int
	requests []string
}

func newFakeVenue(t *testing.T, opts ...Option) (*fakeVenue, *Client) {
	t.Helper()
	v := &fakeVenue{t: t, hmacKey: "nx_test", hmacOwnr: "0x00000000000000000000000000000000000000aa",
		sessions: map[string]string{}, agents: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(v.serve))
	t.Cleanup(srv.Close)
	old := restBases[Local]
	restBases[Local] = srv.URL
	t.Cleanup(func() { restBases[Local] = old })
	c, err := NewClient(Local, append([]Option{WithHTTPClient(srv.Client())}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return v, c
}

func (v *fakeVenue) count() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.requests)
}

func (v *fakeVenue) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	v.mu.Lock()
	defer v.mu.Unlock()
	v.requests = append(v.requests, r.Method+" "+r.URL.Path)
	reply := func(code int, out any) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(out)
	}
	unauthorized := map[string]string{"code": "unauthorized"}
	switch r.URL.Path {
	case "/auth/login":
		var req models.LoginRequest
		_ = json.Unmarshal(body, &req)
		addr, err := recoverHex(signing.PersonalHash(req.Message), req.Signature)
		if req.Message != signInMessage || err != nil {
			reply(401, unauthorized)
			return
		}
		v.logins++
		token := fmt.Sprintf("%064d", v.logins)
		v.sessions[token] = addr
		reply(200, map[string]string{"token": token, "address": addr})
		return
	case "/agents/register":
		var req models.AgentRegistrationRequest
		_ = json.Unmarshal(body, &req)
		agent, _ := signing.ParseAddress(req.Agent)
		d := signing.RegisterAgentDigest(20056, signing.NetworkSalt("local"), agent, uint64(*req.ExpiresAt), uint64(req.Nonce))
		addr, err := recoverHex(d, req.Signature)
		if err != nil || addr != req.Wallet {
			reply(401, map[string]string{"code": "signer_mismatch"})
			return
		}
		v.agents[req.Agent] = req.Wallet
		reply(200, map[string]any{"agent_address": req.Agent, "expires_at": *req.ExpiresAt})
		return
	}

	var owner string
	switch {
	case strings.HasPrefix(r.Header.Get("Authorization"), "Bearer "):
		owner = v.sessions[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	case r.Header.Get("X-Api-Key") == v.hmacKey:
		owner = v.hmacOwnr
	case r.Header.Get("X-Agent") != "":
		ts, _ := strconv.ParseInt(r.Header.Get("X-Timestamp"), 10, 64)
		nonce, _ := strconv.ParseUint(r.Header.Get("X-Nonce"), 10, 64)
		c := signing.AgentCanonical(r.Method, r.URL.Path, r.URL.RawQuery, body, ts, nonce)
		if addr, err := recoverHex(signing.Keccak256([]byte(c)), r.Header.Get("X-Signature")); err == nil && addr == r.Header.Get("X-Agent") {
			owner = v.agents[addr]
		}
	}
	if owner == "" {
		reply(401, unauthorized)
		return
	}
	switch r.URL.Path {
	case "/keys":
		reply(200, []map[string]string{{"key_id": v.hmacKey, "tier": "Pro"}})
	case "/account":
		reply(200, map[string]string{"owner": owner, "balance": "0"})
	default:
		if r.Method == http.MethodGet {
			reply(200, []any{})
		} else {
			reply(200, map[string]any{})
		}
	}
}

func recoverHex(digest []byte, sigHex string) (string, error) {
	sig, err := hex.DecodeString(strings.TrimPrefix(sigHex, "0x"))
	if err != nil {
		return "", err
	}
	return signing.Recover(digest, sig)
}

func testKey(t *testing.T) *PrivateKey {
	t.Helper()
	k, err := GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// Sign-in, then a session read, verified server-side.
func TestSignInSessionRead(t *testing.T) {
	ctx := context.Background()
	wallet := testKey(t)
	_, pub := newFakeVenue(t)
	sess, err := pub.SignIn(ctx, wallet)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Address() != wallet.Address() {
		t.Fatalf("session address = %s, want %s", sess.Address(), wallet.Address())
	}
	if d := time.Until(sess.ExpiresAt()); d < 23*time.Hour || d > 24*time.Hour {
		t.Fatalf("expires in %v, want about 24h", d)
	}
	_, c := newFakeVenue(t, WithSession(sess))
	// A fresh venue does not know this token: its 401 is the server's, and
	// it is not a session expiry.
	if _, err := c.APIKeys(ctx); !errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrSessionExpired) {
		t.Fatalf("unknown token: %v", err)
	}
}

// An expired session fails locally with ErrSessionExpired, which is not a
// 401, and sends nothing. A live one reaches the server.
func TestSessionExpiryIsLocal(t *testing.T) {
	ctx := context.Background()
	v, pub := newFakeVenue(t)
	sess, err := pub.SignIn(ctx, testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := NewClient(Local, WithHTTPClient(http.DefaultClient), WithSession(sess))
	if _, err := c.APIKeys(ctx); err != nil {
		t.Fatalf("live session: %v", err)
	}
	c.clock = func() time.Time { return sess.ExpiresAt().Add(-30 * time.Second) } // inside the margin
	sent, start := v.count(), time.Now()
	_, err = c.APIKeys(ctx)
	// A GET retries with at least 50ms of backoff; a local refusal must not.
	if time.Since(start) > 40*time.Millisecond {
		t.Errorf("local refusal took %v: was it retried?", time.Since(start))
	}
	if !errors.Is(err, ErrSessionExpired) || errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expired session: %v", err)
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		t.Fatalf("expiry surfaced as an APIError: %v", err)
	}
	if v.count() != sent {
		t.Fatal("an expired session was sent")
	}
}

// WithWallet signs in on first use, reuses the session, and signs in again
// once it nears expiry.
func TestWalletRefreshesSession(t *testing.T) {
	ctx := context.Background()
	wallet := testKey(t)
	v, c := newFakeVenue(t, WithWallet(wallet))
	for range 2 {
		if _, err := c.APIKeys(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if v.logins != 1 {
		t.Fatalf("logins = %d, want 1", v.logins)
	}
	c.clock = func() time.Time { return time.Now().Add(sessionTTL) }
	if _, err := c.APIKeys(ctx); err != nil {
		t.Fatal(err)
	}
	if v.logins != 2 {
		t.Fatalf("logins after expiry = %d, want 2", v.logins)
	}
}

// A registered agent trades as its owner and is refused locally, naming
// R2.18, on every withdrawal route.
func TestAgentTradesButCannotWithdraw(t *testing.T) {
	ctx := context.Background()
	wallet, agentKey := testKey(t), testKey(t)
	v, pub := newFakeVenue(t)
	agent, err := pub.RegisterAgent(ctx, wallet, agentKey, RegisterAgentOptions{Label: "bot"})
	if err != nil {
		t.Fatal(err)
	}
	if agent.Owner() != wallet.Address() || agent.Address() != agentKey.Address() {
		t.Fatalf("agent %s owner %s", agent.Address(), agent.Owner())
	}
	if d := time.Until(agent.ExpiresAt()); d < 29*24*time.Hour {
		t.Fatalf("agent expires in %v, want the 30 day default", d)
	}
	c, _ := NewClient(Local, WithHTTPClient(http.DefaultClient), WithAgent(agent))
	if _, err := c.OpenOrders(ctx); err != nil {
		t.Fatalf("agent read: %v", err)
	}
	if _, err := c.CreateOrder(ctx, OrderRequest{MarketId: "BTC-USDX-PERP", Side: Buy, OrderType: OrderTypeLimit}); err != nil {
		t.Fatalf("agent order: %v", err)
	}
	sent := v.count()
	for _, path := range []string{"/withdrawals", "/api/v1/bridge/withdrawals", "/transfers"} {
		err := c.t.Send(ctx, http.MethodPost, path, map[string]string{"amount": "1"}, nil)
		if !errors.Is(err, ErrAgentCannotWithdraw) || !strings.Contains(err.Error(), "R2.18") {
			t.Errorf("POST %s: %v", path, err)
		}
	}
	if v.count() != sent {
		t.Fatal("a refused withdrawal was sent")
	}
}

// Every credential answers "which account am I" (ENG-4641).
func TestAccountAddressEveryCredential(t *testing.T) {
	ctx := context.Background()
	wallet, agentKey := testKey(t), testKey(t)
	v, pub := newFakeVenue(t)
	sess, err := pub.SignIn(ctx, wallet)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := NewAgent(agentKey, wallet.Address())
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := NewAPISecret(strings.Repeat("ab", 32))
	for _, tc := range []struct {
		name string
		opt  Option
		want string
	}{
		{"hmac", WithHMACAuth(v.hmacKey, secret), v.hmacOwnr},
		{"session", WithSession(sess), wallet.Address()},
		{"wallet", WithWallet(wallet), wallet.Address()},
		{"agent", WithAgent(agent), wallet.Address()},
	} {
		c, _ := NewClient(Local, WithHTTPClient(http.DefaultClient), tc.opt)
		for range 2 { // the HMAC answer is fetched once, then kept
			got, err := c.AccountAddress(ctx)
			if err != nil || got != tc.want {
				t.Errorf("%s: AccountAddress = %q, %v; want %q", tc.name, got, err, tc.want)
			}
		}
	}
	if _, err := pub.AccountAddress(ctx); !errors.Is(err, ErrNoCredential) {
		t.Errorf("keyless client: %v", err)
	}
	if n := strings.Count(strings.Join(v.requests, ","), "GET /account"); n != 1 {
		t.Errorf("GET /account sent %d times, want 1", n)
	}
}

func TestOneCredential(t *testing.T) {
	w := testKey(t)
	secret, _ := NewAPISecret(strings.Repeat("ab", 32))
	if _, err := NewClient(Testnet, WithWallet(w), WithHMACAuth("k", secret)); err == nil {
		t.Fatal("two credentials accepted")
	}
	for _, opt := range []Option{WithWallet(nil), WithSession(nil), WithAgent(nil)} {
		if _, err := NewClient(Testnet, opt); err == nil {
			t.Error("nil credential accepted")
		}
	}
}

// Keys and tokens never print.
func TestCredentialsDoNotPrint(t *testing.T) {
	k := testKey(t)
	secretHex := strings.TrimPrefix(k.Hex(), "0x")
	s := &Session{token: func() string { return "tok-secret" }, address: k.Address()}
	type holder struct {
		K *PrivateKey
		S *Session
		k PrivateKey
	}
	h := holder{K: k, S: s, k: *k}
	for _, out := range []string{
		fmt.Sprint(k), fmt.Sprintf("%+v %#v %x %s", k, k, k, k), fmt.Sprintf("%+v", h), fmt.Sprintf("%#v", h),
		fmt.Sprintf("%v %x", s, s),
	} {
		if strings.Contains(out, secretHex) || strings.Contains(out, "tok-secret") {
			t.Fatalf("leaked: %s", out)
		}
	}
	for _, v := range []any{k, s, h} {
		b, _ := json.Marshal(v)
		if strings.Contains(string(b), secretHex) || strings.Contains(string(b), "tok-secret") {
			t.Fatalf("leaked in JSON: %s", b)
		}
	}
	if back, err := NewPrivateKey(k.Hex()); err != nil || back.Address() != k.Address() {
		t.Fatalf("Hex does not round-trip: %v", err)
	}
}
