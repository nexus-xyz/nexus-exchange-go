// Command smoke is the pre-publish smoke test (ENG-18798): one unauthenticated read against the
// public testnet, through the SDK exactly as the release tag will serve it.
//
// scripts/release_gate/smoke.sh builds it from a clean copy of the committed tree (`git archive
// HEAD`), where go.work's `use` of the repo root stands in for the tag, so the import below is what
// a `go get github.com/nexus-xyz/nexus-exchange-go` user gets: no untracked or modified file. The
// read lists markets, the same read every SDK's smoke test makes: Client.FetchMarketsSummary
// (GET /markets/summary), the keyless way to enumerate them.
//
// Three outcomes, kept apart by exit code, because "could not reach testnet" must never read as a
// pass and is not the SDK's fault either:
//
//	0  passed       a list of markets, at least one, each with a market_id
//	1  failed       an answer the SDK could not use: a body it could not decode, a 4xx (a 426
//	                *VersionError among them), a list with no market or a market with no id
//	2  unreachable  no usable answer: a connection, DNS, TLS or timeout error (*url.Error, a
//	                net.Error, the deadline below), or an *APIError the server may recover from
//	                (5xx, 408, 429, 451)
//
// The SDK has already retried a GET that failed with a transport error, a 5xx or a 429 before it
// returns; the deadline bounds those retries.
//
// Each way to an outcome prints its own line, `smoke: <outcome>: <what happened>`, so smoke.sh's
// outcome probes check the line as well as the exit code and a probe can't pass for another reason
// (five paths end in 1, two in 2).
//
// No keys, no writes, no orders. NEXUS_SMOKE_BASE_URL points it elsewhere, for testing the
// outcomes themselves: scheme://host[:port], which replaces the testnet's while the SDK's own path
// is kept (/v1/markets/summary), so the client is still a nexus.Testnet one. The SDK has no custom
// network, so this goes through WithHTTPClient.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	nexus "github.com/nexus-xyz/nexus-exchange-go"
)

// deadline bounds the whole read, the SDK's retries included: a 429's Retry-After may ask for
// minutes, and the job has to end with an outcome before its own timeout.
const deadline = 2 * time.Minute

func main() {
	target := "nexus.Testnet"
	var opts []nexus.Option
	if override := os.Getenv("NEXUS_SMOKE_BASE_URL"); override != "" {
		origin, err := url.Parse(override)
		if err != nil || origin.Scheme == "" || origin.Host == "" || (origin.Path != "" && origin.Path != "/") {
			finish(1, fmt.Sprintf("NEXUS_SMOKE_BASE_URL is not usable: want scheme://host[:port], got %q", override))
		}
		target = origin.Scheme + "://" + origin.Host
		opts = append(opts, nexus.WithHTTPClient(&http.Client{
			Timeout:   30 * time.Second,
			Transport: redirect{origin: origin},
		}))
	}

	client, err := nexus.NewClient(nexus.Testnet, opts...)
	if err != nil {
		finish(1, fmt.Sprintf("NewClient(nexus.Testnet): %v", err))
	}
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	markets, err := client.FetchMarketsSummary(ctx)
	if err != nil {
		if unreachable(err) {
			// Two lines, so smoke.sh can tell an outage from nothing listening.
			var apiErr *nexus.APIError
			if errors.As(err, &apiErr) {
				finish(2, fmt.Sprintf("%s answered HTTP %d, which it may recover from: %v", target, apiErr.StatusCode, err))
			}
			finish(2, fmt.Sprintf("%s gave no usable answer: %v", target, err))
		}
		finish(1, fmt.Sprintf("FetchMarketsSummary against %s failed: %v", target, err))
	}
	if len(markets) == 0 {
		finish(1, "FetchMarketsSummary decoded an EMPTY list from "+target)
	}
	for i, m := range markets {
		if m.MarketId == nil || *m.MarketId == "" {
			finish(1, fmt.Sprintf("FetchMarketsSummary from %s is not a markets list: entry %d has no market_id", target, i))
		}
	}
	finish(0, fmt.Sprintf("FetchMarketsSummary decoded %d markets from %s (first: %s)", len(markets), target, *markets[0].MarketId))
}

// unreachable reports whether err means no usable answer came back, as opposed to an answer the
// SDK could not use.
func unreachable(err error) bool {
	var apiErr *nexus.APIError
	if errors.As(err, &apiErr) {
		switch s := apiErr.StatusCode; {
		case s >= 500, s == http.StatusRequestTimeout, s == http.StatusTooManyRequests,
			s == http.StatusUnavailableForLegalReasons:
			return true
		default:
			return false
		}
	}
	// What http.Client.Do returns for a connection, DNS, TLS or timeout failure is a *url.Error;
	// a body cut short reads as a net.Error or an unexpected EOF. A decode error is none of these.
	var urlErr *url.Error
	var netErr net.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &urlErr) ||
		errors.As(err, &netErr) || errors.Is(err, io.ErrUnexpectedEOF)
}

// redirect sends every request to origin, keeping the SDK's path and query.
type redirect struct{ origin *url.URL }

func (r redirect) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host, req.Host = r.origin.Scheme, r.origin.Host, r.origin.Host
	return http.DefaultTransport.RoundTrip(req)
}

func finish(code int, message string) {
	outcome := map[int]string{0: "passed", 2: "unreachable"}[code]
	if outcome == "" {
		outcome = "failed"
	}
	fmt.Printf("smoke: %s: %s\n", outcome, message)
	os.Exit(code)
}
