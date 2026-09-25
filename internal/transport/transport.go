package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"time"

	"github.com/nexus-xyz/nexus-exchange-go/internal/signing"
)

const modulePath = "github.com/nexus-xyz/nexus-exchange-go"

// maxBody bounds how much of a response is read, so a hostile or broken
// upstream cannot exhaust memory.
const maxBody = 16 << 20

// Transport sends requests to one REST base and decodes the responses. It is
// safe for concurrent use.
//
// Every request path is relative to the base and is the spec's bare path, for
// example "/orders". That path, not the full URL, is what a request signer
// signs: the base's own prefix (/v1 on the public hosts) is stripped at the
// edge before the server verifies.
type Transport struct {
	base       string
	basePath   string // base's own path prefix ("/v1"), not signed
	http       *http.Client
	userAgent  string
	apiVersion string

	// Retry policy for GET only. Fields rather than constants so tests can
	// shrink the delays.
	maxRetries int
	minDelay   time.Duration
	maxDelay   time.Duration

	// Refuse, when set, is returned by every request before any network I/O.
	// NewClient sets it for Mainnet.
	Refuse error

	// Signer, when set, signs every request (every attempt, so a retried GET
	// carries a fresh timestamp).
	Signer Signer

	// limits paces requests on the budgets the server reports (ratelimit.go).
	limits limiter
}

// Signer authenticates one request attempt by setting headers on h. path is
// the signed path (without the base prefix) and query the raw query string.
// An error aborts the request before any network I/O and is not retried.
type Signer interface {
	Sign(ctx context.Context, h http.Header, method, path, query string, body []byte) error
}

// clocked is a Signer that stamps requests with its own clock, so a 401 can
// report how far that clock is from the server's.
type clocked interface{ Clock() time.Time }

// localError is a refusal made before any bytes left the process. Get never
// retries it: the same request is refused the same way again.
type localError struct{ error }

func (e localError) Unwrap() error { return e.error }

// Compile-time checks that the signers satisfy Signer.
var (
	_ Signer = (*signing.HMAC)(nil)
	_ Signer = (*signing.Agent)(nil)
)

// New returns a Transport for base (no trailing slash) sending through hc.
// apiVersion is sent on every request as X-Nexus-Api-Version.
func New(base string, hc *http.Client, apiVersion string) *Transport {
	base = strings.TrimRight(base, "/")
	var basePath string
	if u, err := url.Parse(base); err == nil {
		basePath = u.EscapedPath()
	}
	return &Transport{
		base:       base,
		basePath:   basePath,
		http:       hc,
		userAgent:  userAgent(),
		apiVersion: apiVersion,
		maxRetries: 3,
		minDelay:   100 * time.Millisecond,
		maxDelay:   5 * time.Second,
	}
}

// Get sends an idempotent GET and decodes a 2xx JSON body into out (unless out
// is nil). It is the only method that retries, up to maxRetries times:
// transport failures and 5xx responses other than 501 and 505 with jittered
// exponential backoff, and a RATE_LIMIT_EXCEEDED 429 after its retry-after
// (never below one second) plus jitter.
func (t *Transport) Get(ctx context.Context, path string, query url.Values, out any) error {
	if t.Refuse != nil {
		return t.Refuse
	}
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	delay := t.minDelay
	for attempt := 0; ; attempt++ {
		err := t.do(ctx, http.MethodGet, path, nil, out)
		if attempt == t.maxRetries || !retryable(ctx, err) {
			return err
		}
		// Jitter: sleep a random duration in [delay/2, delay].
		sleep := delay/2 + rand.N(delay/2+1)
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests {
			sleep = retryAfterDelay(apiErr.RetryAfter)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sleep):
		}
		delay = min(delay*2, t.maxDelay)
	}
}

// Send makes one request that may change state (POST, PUT, PATCH, DELETE) and
// decodes a 2xx JSON body into out (unless out is nil). It never retries, on
// any outcome: after a lost response the SDK cannot know whether the server
// acted, and resubmitting an order is worse than failing it.
func (t *Transport) Send(ctx context.Context, method, path string, body, out any) error {
	if t.Refuse != nil {
		return t.Refuse
	}
	if method == http.MethodGet {
		return errors.New("nexus: Send is for mutations; use Get")
	}
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			return fmt.Errorf("nexus: encode request: %w", err)
		}
	}
	return t.do(ctx, method, path, raw, out)
}

func (t *Transport) do(ctx context.Context, method, path string, body []byte, out any) error {
	class, weight := costOf(method, path, body)
	if b := t.limits.of(class); b != nil {
		if err := b.wait(ctx, weight); err != nil {
			return err
		}
	}
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, t.base+path, r)
	if err != nil {
		return fmt.Errorf("nexus: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", t.userAgent)
	req.Header.Set("X-Nexus-Api-Version", t.apiVersion)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if t.Signer != nil {
		// Sign what goes on the wire, minus the base prefix the edge strips.
		signed := strings.TrimPrefix(req.URL.EscapedPath(), t.basePath)
		if err := t.Signer.Sign(ctx, req.Header, method, signed, req.URL.RawQuery, body); err != nil {
			return localError{err}
		}
	}
	resp, err := t.http.Do(req)
	if err != nil {
		return fmt.Errorf("nexus: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if sink, ok := ctx.Value(headerSink{}).(*http.Header); ok {
		*sink = resp.Header
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("nexus: %s %s: read response: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		e := decodeError(resp.StatusCode, resp.Header, data)
		t.limits.observe(class, resp.Header, e, time.Now())
		if c, ok := t.Signer.(clocked); ok && resp.StatusCode == http.StatusUnauthorized {
			estimateSkew(e, resp.Header, c.Clock())
		}
		if resp.StatusCode == http.StatusUpgradeRequired {
			return newVersionError(e, t.apiVersion, resp.Header)
		}
		return e
	}
	t.limits.observe(class, resp.Header, nil, time.Now())
	if out == nil || len(data) == 0 {
		return nil
	}
	if err := UnmarshalNumbers(data, out); err != nil {
		return fmt.Errorf("nexus: %s %s: decode response: %w", method, path, err)
	}
	return nil
}

// UnmarshalNumbers is json.Unmarshal, except that a JSON number landing in an
// interface{} (an orderbook level, a CCXT info map) decodes as json.Number
// rather than float64, so no served digit is lost there either.
func UnmarshalNumbers(data []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("invalid character after top-level value")
	}
	return nil
}

// estimateSkew records how far the server's Date header is from the clock the
// signer stamped with. It says nothing about why the server refused: a 401 is
// opaque (R2.12), and skew is one fact among several possible causes.
func estimateSkew(e *APIError, h http.Header, now time.Time) {
	d, err := http.ParseTime(h.Get("Date"))
	if err != nil {
		return
	}
	e.ServerTime = d
	// Date has one-second resolution, so finer precision would be invented.
	e.ClockSkew = d.Sub(now).Round(time.Second)
}

// retryable reports whether a GET that failed with err may succeed if sent
// again: a 5xx the server may recover from, a RATE_LIMIT_EXCEEDED 429 (the
// other 429 codes are caps, not budgets that refill), or a transport failure
// that was not the caller cancelling.
func retryable(ctx context.Context, err error) bool {
	var local localError
	if err == nil || ctx.Err() != nil || errors.As(err, &local) {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		s := apiErr.StatusCode
		if s == http.StatusTooManyRequests {
			return apiErr.Code == "" || apiErr.Code == "RATE_LIMIT_EXCEEDED"
		}
		return s >= 500 && s != http.StatusNotImplemented && s != http.StatusHTTPVersionNotSupported
	}
	// Decode failures are not transient; the same bytes fail again.
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	return !errors.As(err, &syntaxErr) && !errors.As(err, &typeErr)
}

// userAgent is nexus-exchange-go/<version>, the product token the indexer
// classifies as the go client (ENG-16563). The version comes from the build
// info of whichever binary embeds this module.
func userAgent() string {
	v := "devel"
	if bi, ok := debug.ReadBuildInfo(); ok {
		mods := append([]*debug.Module{&bi.Main}, bi.Deps...)
		for _, m := range mods {
			if m.Path == modulePath && m.Version != "" && m.Version != "(devel)" {
				v = strings.TrimPrefix(m.Version, "v")
			}
		}
	}
	return "nexus-exchange-go/" + v
}
