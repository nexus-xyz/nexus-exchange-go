package nexus

import "github.com/nexus-xyz/nexus-exchange-go/internal/transport"

// APIError is the error returned for every non-2xx response. Reach it with
// errors.As:
//
//	var apiErr *nexus.APIError
//	if errors.As(err, &apiErr) {
//		log.Println(apiErr.StatusCode, apiErr.Code, apiErr.Message, apiErr.Details)
//	}
//
// Code is exactly what the server sent. Codes are not consistently cased
// (RATE_LIMIT_EXCEEDED beside destination_locked), and the SDK does not
// normalise them, so compare against the contract's spelling.
//
// A 401 is opaque by design: bad key, bad signature, stale timestamp and
// expired session all look the same, and the SDK does not guess which. A 404
// on an authenticated read may mean the resource is not yours; it is reported
// as a 404 and nothing friendlier.
type APIError = transport.APIError

// Sentinel errors for the conditions callers branch on. Match them with
// errors.Is; an *APIError satisfies the one that fits it.
var (
	// ErrUnauthorized matches any 401. The cause is deliberately not exposed.
	ErrUnauthorized = transport.ErrUnauthorized
	// ErrNotFound matches any 404, including "exists but is not yours".
	ErrNotFound = transport.ErrNotFound
	// ErrRateLimited matches any 429. The SDK does not retry it; honour
	// APIError.RetryAfter and see APIError.Bucket for which budget refused.
	ErrRateLimited = transport.ErrRateLimited
	// ErrMarginUnavailable matches code authoritative_margin_unavailable: the
	// engine's margin view is down and the endpoint failed closed. Retry the
	// read later, and never read it as an empty account.
	ErrMarginUnavailable = transport.ErrMarginUnavailable
)
