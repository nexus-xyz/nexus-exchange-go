package nexus

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/nexus-xyz/nexus-exchange-go/internal/models"
	"github.com/nexus-xyz/nexus-exchange-go/internal/signing"
)

// signInMessage is the fixed text POST /auth/login verifies. It carries no
// nonce, so a signature over it is a reusable credential: never log it.
const signInMessage = "Sign in to Nexus Exchange"

// sessionTTL is the server's session lifetime (SESSION_TTL_SECS). The login
// response does not carry an expiry, so the SDK counts it itself.
const sessionTTL = 24 * time.Hour

// sessionMargin is how early a session is treated as expired, so a request
// signed just before the deadline does not arrive just after it.
const sessionMargin = time.Minute

// ErrSessionExpired is returned, before any network I/O, by a request on a
// [WithSession] client whose session has passed its expiry. It is local: it
// never matches [ErrUnauthorized], because the server's 401 cannot say a
// session expired (R2.12) and the SDK does not guess. Sign in again with
// [Client.SignIn], or use [WithWallet], which does that for you.
var ErrSessionExpired = errors.New("nexus: session expired")

// Session is a bearer session from wallet sign-in ([Client.SignIn]). The
// token is a full-authority wallet credential, valid only on the network that
// minted it, and it cannot be printed: fmt and JSON show the address only.
type Session struct {
	token     func() string
	address   string
	expiresAt time.Time
}

// Address is the wallet the server recovered from the sign-in signature:
// the account this session acts for.
func (s *Session) Address() string { return s.address }

// ExpiresAt is when the session lapses, counted from just before the
// sign-in request was sent. A request after ExpiresAt minus one minute fails
// locally with [ErrSessionExpired].
func (s *Session) ExpiresAt() time.Time { return s.expiresAt }

func (s *Session) String() string   { return "nexus.Session(" + s.address + ")" }
func (s *Session) GoString() string { return s.String() }

// Format shows only the address, under every verb.
func (s *Session) Format(f fmt.State, _ rune) { fmt.Fprint(f, s.String()) }

// MarshalJSON emits the address, never the token.
func (s *Session) MarshalJSON() ([]byte, error) { return []byte(`"` + s.String() + `"`), nil }

// SignIn signs the fixed sign-in message with wallet (EIP-191 personal_sign)
// and exchanges it for a session (POST /auth/login). Pass the session to
// [WithSession]. It needs no credential on c.
//
// It is sent once and not retried; the login budget is small and a 429
// carries Retry-After.
func (c *Client) SignIn(ctx context.Context, wallet *PrivateKey) (*Session, error) {
	if wallet == nil {
		return nil, errors.New("nexus: SignIn needs a wallet key")
	}
	start := c.now()
	sig := signing.SignHash(wallet.key(), signing.PersonalHash(signInMessage))
	body := models.LoginRequest{Message: signInMessage, Signature: "0x" + hex.EncodeToString(sig)}
	var out models.LoginResponse
	if err := c.pub.Send(ctx, http.MethodPost, "/auth/login", body, &out); err != nil {
		return nil, err
	}
	if out.Token == nil || *out.Token == "" || out.Address == nil {
		return nil, errors.New("nexus: POST /auth/login returned no token")
	}
	token := *out.Token
	return &Session{token: func() string { return token }, address: *out.Address, expiresAt: start.Add(sessionTTL)}, nil
}

// bearer signs requests with a session. With a wallet it signs in on first
// use and again when the session nears expiry; without one it refuses once
// the session lapses.
type bearer struct {
	now    func() time.Time
	signIn func(context.Context) (*Session, error) // nil: no refresh

	// ponytail: one lock around sign-in, so a refresh briefly holds every
	// request on this client (once a day). Fine for a bot; revisit if it shows.
	mu      sync.Mutex
	session *Session
}

func (b *bearer) current(ctx context.Context) (*Session, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.session != nil && b.now().Add(sessionMargin).Before(b.session.expiresAt) {
		return b.session, nil
	}
	if b.signIn == nil {
		return nil, fmt.Errorf("%w at %s: sign in again", ErrSessionExpired, b.session.expiresAt.Format(time.RFC3339))
	}
	s, err := b.signIn(ctx)
	if err != nil {
		return nil, fmt.Errorf("nexus: session refresh: %w", err)
	}
	b.session = s
	return s, nil
}

func (b *bearer) Sign(ctx context.Context, h http.Header, _, _, _ string, _ []byte) error {
	s, err := b.current(ctx)
	if err != nil {
		return err
	}
	h.Set("Authorization", "Bearer "+s.token())
	return nil
}

// APIKey is a newly minted HMAC key from [Client.CreateAPIKey]. The secret is
// shown once and never again: store it, or pass both halves straight to
// [WithHMACAuth].
type APIKey struct {
	KeyID  string
	Secret APISecret
}

// APIKeyInfo is one key from [Client.APIKeys]. The secret is never listed.
//
// Hand-written: the pinned spec gives these responses an example but no
// schema, so there is nothing to generate.
type APIKeyInfo struct {
	KeyID string `json:"key_id"`
	Tier  string `json:"tier"`
}

// CreateAPIKey mints an HMAC API key for the signed-in wallet (POST /keys,
// bearerAuth). The key is valid only on this client's network.
func (c *Client) CreateAPIKey(ctx context.Context) (*APIKey, error) {
	var out struct {
		KeyID  string `json:"key_id"`
		Secret string `json:"secret"`
	}
	if err := c.t.Send(ctx, http.MethodPost, "/keys", nil, &out); err != nil {
		return nil, err
	}
	secret, err := NewAPISecret(out.Secret)
	if err != nil {
		return nil, fmt.Errorf("nexus: POST /keys: %w", err)
	}
	return &APIKey{KeyID: out.KeyID, Secret: secret}, nil
}

// APIKeys lists the signed-in wallet's API keys on this network (GET /keys,
// bearerAuth).
func (c *Client) APIKeys(ctx context.Context) ([]APIKeyInfo, error) {
	var out []APIKeyInfo
	if err := c.t.Get(ctx, "/keys", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteAPIKey revokes one of the signed-in wallet's keys (DELETE
// /keys/{key_id}, bearerAuth). A 404 does not prove the key is gone: a key
// minted on another network is invisible here.
func (c *Client) DeleteAPIKey(ctx context.Context, keyID string) error {
	return c.t.Send(ctx, http.MethodDelete, "/keys/"+url.PathEscape(keyID), nil, nil)
}
