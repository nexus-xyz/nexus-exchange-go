package signing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// MaxDrift is how far x-timestamp may be from the server's clock, either way,
// before the server rejects the request. It mirrors HMAC_MAX_DRIFT_MS in the
// server's verifier (nexus_accounts::hmac_verify).
const MaxDrift = 30 * time.Second

// HMAC signs requests with an API key (the spec's hmacAuth scheme).
type HMAC struct {
	KeyID string
	// Secret is the decoded key bytes, never the issued hex text.
	Secret []byte
	// Now is the clock the timestamp is read from; nil means time.Now.
	Now func() time.Time
}

// Canonical is the string the server recomputes and verifies:
//
//	ts \n METHOD \n path \n query \n hex(sha256(body))
//
// ts is Unix milliseconds in decimal. path is the path the server receives
// (after the edge strips its prefix), and query is the raw query string
// exactly as sent, without "?". Neither is re-encoded or sorted: the server
// hashes the bytes it received.
func Canonical(tsMillis int64, method, path, query string, body []byte) string {
	sum := sha256.Sum256(body)
	return strconv.FormatInt(tsMillis, 10) + "\n" + strings.ToUpper(method) + "\n" +
		path + "\n" + query + "\n" + hex.EncodeToString(sum[:])
}

// Sign returns the lowercase hex HMAC-SHA256 of canonical under secret.
func Sign(secret []byte, canonical string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(canonical))
	return hex.EncodeToString(m.Sum(nil))
}

// Apply sets x-api-key, x-timestamp and x-signature on h for a request.
func (s *HMAC) Apply(h http.Header, method, path, query string, body []byte) {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	ts := now().UnixMilli()
	h.Set("X-Api-Key", s.KeyID)
	h.Set("X-Timestamp", strconv.FormatInt(ts, 10))
	h.Set("X-Signature", Sign(s.Secret, Canonical(ts, method, path, query, body)))
}
