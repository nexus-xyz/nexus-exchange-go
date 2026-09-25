package nexus

import (
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
}

// Option configures a [Client].
type Option func(*config)

type config struct {
	httpClient *http.Client

	keyID   string
	secret  APISecret
	hmacSet bool
}

// WithHTTPClient makes the client send through hc, so callers can bring their
// own transport, proxy, timeout or instrumentation. The default is an
// *http.Client with a 30 second timeout.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *config) { c.httpClient = hc }
}

// NewClient returns a client for network. It returns an error if network is
// not one of [Mainnet], [Testnet] or [Local]; there is no default network.
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
	signer, err := cfg.signer()
	if err != nil {
		return nil, err
	}
	t := transport.New(base, cfg.httpClient, APIVersion())
	t.Signer = signer
	if network == Mainnet {
		t.Refuse = ErrMainnetNotTargetable
	}
	return &Client{t: t}, nil
}
