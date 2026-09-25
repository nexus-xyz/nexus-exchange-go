package nexus

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/nexus-xyz/nexus-exchange-go/internal/transport"
)

// defaultTimeout bounds each request when the caller does not supply an
// *http.Client of their own, matching the Rust SDK.
const defaultTimeout = 30 * time.Second

// Client is a Nexus Exchange API client bound to one [Network]. It is safe for
// concurrent use.
//
// Every call takes a [context.Context] as its first argument; cancelling it
// aborts the request in flight, or the wait before it. Only GET requests are
// retried, at most 3 times: transport failures and 5xx with jittered backoff,
// and a RATE_LIMIT_EXCEEDED 429 after its Retry-After (never below one
// second) plus jitter. A
// request that can change state is sent exactly once, whatever the outcome,
// because a duplicate order is worse than a failed one; its 429 is returned as
// an [*APIError] carrying Retry-After.
//
// A client holds at most one credential, chosen at construction:
// [WithHMACAuth], [WithSession], [WithWallet] or [WithAgent]. Whichever it is,
// [Client.AccountAddress] says which account the client acts for.
//
// # Rate limits
//
// The client paces itself on the budgets the server reports in its
// x-ratelimit-* headers, one budget at a time: reads, order submission and
// sign-in each wait only on their own. Each call is charged its weight, read
// from the pinned spec's x-nexus-rate-limit-weight markers rather than typed
// from the documentation, so a heavy read (GET /fills, weight 5) spends five
// times what a ticker read does, and a remaining of 10 is two of them. Until a
// response has reported a budget nothing is paced. Cancels are never paced:
// see [Client.CancelOrder].
type Client struct {
	t *transport.Transport
	// pub sends the operations authorised by a signature in the body rather
	// than a header (sign-in, agent registration). It carries no credential.
	pub     *transport.Transport
	network Network
	// account answers AccountAddress for the configured credential.
	account func(context.Context) (string, error)
	// clock, when set, replaces time.Now for session and agent timing.
	clock func() time.Time
}

func (c *Client) now() time.Time {
	if c.clock != nil {
		return c.clock()
	}
	return time.Now()
}

// Option configures a [Client].
type Option func(*config)

type config struct {
	httpClient *http.Client
	// creds holds one entry per credential option. Each installs its signer
	// and its answer to AccountAddress; NewClient allows at most one.
	creds []func(*Client) error
}

// WithHTTPClient makes the client send through hc, so callers can bring their
// own transport, proxy, timeout or instrumentation. The default is an
// *http.Client with a 30 second timeout.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *config) { c.httpClient = hc }
}

// NewClient returns a client for network. It returns an error if network is
// not one of [Mainnet], [Testnet] or [Local]; there is no default network. It
// also returns an error if more than one credential option is given.
//
// A Mainnet client is built, but every request through it fails locally with
// [ErrMainnetNotTargetable] until api.nexus.xyz resolves (ENG-15183).
func NewClient(network Network, opts ...Option) (*Client, error) {
	base, ok := restBases[network]
	if !ok {
		return nil, fmt.Errorf("nexus: network %v is not set; choose Mainnet, Testnet or Local explicitly", network)
	}
	cfg := config{httpClient: &http.Client{Timeout: defaultTimeout}}
	for _, opt := range opts {
		opt(&cfg)
	}
	if len(cfg.creds) > 1 {
		return nil, errors.New("nexus: choose one credential: WithHMACAuth, WithSession, WithWallet or WithAgent")
	}
	c := &Client{
		t:       transport.New(base, cfg.httpClient, APIVersion()),
		pub:     transport.New(base, cfg.httpClient, APIVersion()),
		network: network,
		account: func(context.Context) (string, error) { return "", ErrNoCredential },
	}
	for _, install := range cfg.creds {
		if err := install(c); err != nil {
			return nil, err
		}
	}
	if network == Mainnet {
		c.t.Refuse, c.pub.Refuse = ErrMainnetNotTargetable, ErrMainnetNotTargetable
	}
	return c, nil
}

// ErrNoCredential is returned by [Client.AccountAddress] on a client built
// without a credential: a keyless client acts for no account.
var ErrNoCredential = errors.New("nexus: client has no credential, so it acts for no account")

// AccountAddress returns the address of the account this client acts for,
// lower-case hex with 0x, whatever the credential (ENG-4641):
//
//   - [WithWallet]: the wallet's address, without a request.
//   - [WithSession]: the address the server recovered at sign-in, without a
//     request.
//   - [WithAgent]: the owner wallet the agent was registered to, not the
//     agent's own address, without a request.
//   - [WithHMACAuth]: an API key does not carry its owner, so the first call
//     asks the server and the answer is kept for the life of the client. The
//     route is GET /account/deposit-target, whose account field the indexer
//     answers without the engine; it is a stand-in until GET /whoami
//     (ENG-17767) ships in a spec release. If that route refuses (403 on an
//     early-access deployment, 503 on a misconfigured deposit target), the
//     error matches [ErrAccountUnresolved] and wraps the [*APIError].
func (c *Client) AccountAddress(ctx context.Context) (string, error) {
	return c.account(ctx)
}

// WithSession authenticates every request with s (bearerAuth). Once s
// expires, requests fail locally with [ErrSessionExpired] rather than being
// sent to collect an opaque 401; sign in again for a new session, or use
// [WithWallet] to have that done automatically.
func WithSession(s *Session) Option {
	return func(cfg *config) {
		cfg.creds = append(cfg.creds, func(c *Client) error {
			if s == nil {
				return errors.New("nexus: WithSession needs a session from Client.Login")
			}
			c.t.Signer = &bearer{now: c.now, session: s}
			c.account = func(context.Context) (string, error) { return s.Address(), nil }
			return nil
		})
	}
}

// WithWallet authenticates as the wallet itself: the client signs in with
// wallet on first use and again a minute before each session expires, so it
// never presents an expired session. The session is full authority over the
// account, including funds; for a long-running bot prefer [WithAgent].
func WithWallet(wallet *PrivateKey) Option {
	return func(cfg *config) {
		cfg.creds = append(cfg.creds, func(c *Client) error {
			if wallet == nil {
				return errors.New("nexus: WithWallet needs a key from NewPrivateKey")
			}
			c.t.Signer = &bearer{now: c.now, signIn: func(ctx context.Context) (*Session, error) {
				return c.Login(ctx, wallet)
			}}
			c.account = func(context.Context) (string, error) { return wallet.Address(), nil }
			return nil
		})
	}
}

// WithAgent signs every request with a registered agent key (agentAuth).
// The agent trades for its owner's account and is refused locally, with
// [ErrAgentCannotWithdraw], on every route that moves funds off it (R2.18).
func WithAgent(a *Agent) Option {
	return func(cfg *config) {
		cfg.creds = append(cfg.creds, func(c *Client) error {
			if a == nil {
				return errors.New("nexus: WithAgent needs an agent from RegisterAgent or NewAgent")
			}
			c.t.Signer = a.signer
			c.account = func(context.Context) (string, error) { return a.Owner(), nil }
			return nil
		})
	}
}
