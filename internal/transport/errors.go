package transport

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Sentinels for the conditions callers branch on. Re-exported by package nexus.
var (
	ErrUnauthorized      = errors.New("nexus: unauthorized")
	ErrNotFound          = errors.New("nexus: not found")
	ErrRateLimited       = errors.New("nexus: rate limited")
	ErrMarginUnavailable = errors.New("nexus: authoritative margin unavailable")

	ErrMainnetNotTargetable = errors.New("nexus: Mainnet is not targetable by this release: " +
		"api.nexus.xyz does not resolve yet (ENG-15183). Requests are refused locally rather than " +
		"sent to a real-funds host on a guessed URL; use Testnet, or Local for an indexer you run")
)

// maxRetryAfter caps a server-advised delay, as the Rust SDK does, so a broken
// or hostile upstream cannot park a caller forever.
const maxRetryAfter = 5 * time.Minute

// APIError is a non-2xx response from the API.
type APIError struct {
	// StatusCode is the HTTP status.
	StatusCode int
	// Code is the machine-readable code exactly as served, never re-cased.
	// Empty when the body was not an API error envelope (an edge 404, say);
	// Message then holds the body text.
	Code string
	// Message is human-readable detail. Do not branch on it.
	Message string
	// Details holds the envelope's details object plus any other top-level
	// keys the server sent (claimed and recovered on SIGNER_MISMATCH, for
	// example). Values are raw JSON so no number loses precision.
	Details map[string]json.RawMessage

	// RetryAfter is the 429 Retry-After header in seconds, capped at five
	// minutes; zero when absent or not in seconds.
	RetryAfter time.Duration
	// Bucket is the 429 budget that refused the request (key, owner, order,
	// cancel, ip, login). Buckets are independent: do not back off the whole
	// client on one of them.
	Bucket string
	// Tier is the rate-limit tier the refused request was charged against.
	Tier string

	// ServerTime is the Date header of a 401 to a signed request; zero when
	// the response had none. ClockSkew is ServerTime minus the local clock
	// (positive: the server is ahead), to the second. The signature is
	// accepted only within 30 seconds of server time, so a large skew is worth
	// checking, but the 401 itself does not say it was the cause.
	ServerTime time.Time
	ClockSkew  time.Duration
}

func (e *APIError) Error() string {
	s := "nexus: HTTP " + strconv.Itoa(e.StatusCode)
	if e.Code != "" {
		s += " " + e.Code
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	if !e.ServerTime.IsZero() {
		s += " (server clock differs from local by " + e.ClockSkew.String() +
			"; signatures are accepted within 30s; the 401 does not say which check failed)"
	}
	return s
}

// Is makes the sentinels match with errors.Is. Status-based sentinels match on
// status alone, so the code's casing can never cause a miss.
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrUnauthorized:
		return e.StatusCode == http.StatusUnauthorized
	case ErrNotFound:
		return e.StatusCode == http.StatusNotFound
	case ErrRateLimited:
		return e.StatusCode == http.StatusTooManyRequests
	case ErrMarginUnavailable:
		return e.Code == "authoritative_margin_unavailable"
	}
	return false
}

// decodeError builds an APIError from a non-2xx response. Three envelope
// shapes are live on the wire and all are accepted:
//
//	{"code": "X", "message": "..."}                     the standard envelope
//	{"error": "X", "message": "..."}                    legacy; also the WithdrawalError anyOf
//	{"error": {"code": "X", "message": "...", ...}}     BridgeError
//
// The engine sends code and error together with the same value (ENG-4644);
// code wins when both are present.
func decodeError(status int, h http.Header, body []byte) *APIError {
	e := &APIError{StatusCode: status}
	if status == http.StatusTooManyRequests {
		if secs, err := strconv.Atoi(strings.TrimSpace(h.Get("Retry-After"))); err == nil && secs >= 0 {
			e.RetryAfter = min(time.Duration(secs)*time.Second, maxRetryAfter)
		}
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		e.Message = strings.TrimSpace(string(body))
		return e
	}
	take := func(key string) (s string) {
		if raw, ok := fields[key]; ok && json.Unmarshal(raw, &s) == nil {
			delete(fields, key)
		}
		return s
	}
	e.Code = take("code")
	legacy := take("error")
	e.Message = take("message")
	e.Bucket = take("bucket")
	e.Tier = take("tier")
	var nested struct {
		Code    string                     `json:"code"`
		Message string                     `json:"message"`
		Details map[string]json.RawMessage `json:"details"`
	}
	if raw, ok := fields["error"]; ok && json.Unmarshal(raw, &nested) == nil {
		delete(fields, "error")
		legacy = nested.Code
		if e.Message == "" {
			e.Message = nested.Message
		}
		fields = merge(fields, nested.Details)
	}
	if e.Code == "" {
		e.Code = legacy
	}
	if raw, ok := fields["details"]; ok {
		var d map[string]json.RawMessage
		if json.Unmarshal(raw, &d) == nil {
			delete(fields, "details")
			fields = merge(fields, d)
		}
	}
	if len(fields) > 0 {
		e.Details = fields
	}
	return e
}

func merge(dst, src map[string]json.RawMessage) map[string]json.RawMessage {
	for k, v := range src {
		if _, ok := dst[k]; !ok {
			dst[k] = v
		}
	}
	return dst
}
