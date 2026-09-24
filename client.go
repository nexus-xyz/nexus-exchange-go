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
// aborts the request in flight. Only GET requests are retried (transport
// failures and 5xx, at most 3 times, with jittered backoff). A request that can
// change state is sent exactly once, whatever the outcome, because a duplicate
// order is worse than a failed one. A 429 is never retried: it is returned as
// an [*APIError] carrying Retry-After.
type Client struct {
	t *transport.Transport
}

// Option configures a [Client].
type Option func(*config)

type config struct {
	httpClient *http.Client
}

// WithHTTPClient makes the client send through hc, so callers can bring their
// own transport, proxy, timeout or instrumentation. The default is an
// *http.Client with a 30 second timeout.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *config) { c.httpClient = hc }
}

// NewClient returns a client for network. It returns an error if network is
// not one of [Mainnet], [Testnet] or [Local]; there is no default network.
func NewClient(network Network, opts ...Option) (*Client, error) {
	base, ok := restBases[network]
	if !ok {
		return nil, fmt.Errorf("nexus: network %v is not set; choose Mainnet, Testnet or Local explicitly", network)
	}
	cfg := config{httpClient: &http.Client{Timeout: defaultTimeout}}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &Client{t: transport.New(base, cfg.httpClient, APIVersion())}, nil
}
