package nexus

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/nexus-xyz/nexus-exchange-go/internal/signing"
)

const redacted = "nexus.APISecret(REDACTED)"

// APISecret is the secret half of an API key, held as the decoded bytes the
// server signs with. The zero value holds no key; build one with
// [NewAPISecret].
//
// It cannot be printed: every fmt verb, JSON encoding and a nested %+v of a
// struct holding it all produce a fixed placeholder.
type APISecret struct {
	// A func rather than the bytes: when fmt reflects into a struct holding
	// an APISecret in an unexported field it cannot call Format, and it
	// prints a func as an address. Bytes, an array or a pointer to one would
	// be printed in full.
	key func() []byte
}

// NewAPISecret decodes secretHex, the hex text issued with the key (POST
// /keys), into the raw bytes that requests are signed with; it returns an
// error if secretHex is not hex or does not decode to 32 bytes.
//
// Signing with the hex text itself is the classic failure (ENG-14662): every
// signed call returns 401 and nothing says why. That is why this is the only
// way to build an APISecret and why it takes the issued text, not bytes. A
// leading "0x" is accepted, as the server's own key seeding accepts it.
func NewAPISecret(secretHex string) (APISecret, error) {
	b, err := hex.DecodeString(strings.TrimPrefix(secretHex, "0x"))
	if err != nil {
		// The input is not echoed: it may be a real secret, mistyped.
		return APISecret{}, errors.New("nexus: API secret is not hex; pass the secret exactly as issued")
	}
	if len(b) != 32 {
		return APISecret{}, fmt.Errorf("nexus: API secret decodes to %d bytes, want 32", len(b))
	}
	return APISecret{key: func() []byte { return b }}, nil
}

func (APISecret) String() string   { return redacted }
func (APISecret) GoString() string { return redacted }

// Format redacts under every verb, including %x and %d.
func (APISecret) Format(f fmt.State, _ rune) { fmt.Fprint(f, redacted) }

// MarshalJSON emits the placeholder, so a config struct dumped as JSON does
// not leak the key.
func (APISecret) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }

// WithHMACAuth signs every request with the API key keyID and its secret
// (the hmacAuth scheme: x-api-key, x-timestamp, x-signature). The timestamp
// is this machine's clock and must be within 30 seconds of the server's; on a
// 401 the error reports the measured skew when the server sent a Date header.
//
// NewClient returns an error if secret is the zero APISecret or keyID is
// empty.
func WithHMACAuth(keyID string, secret APISecret) Option {
	return func(cfg *config) {
		cfg.creds = append(cfg.creds, func(c *Client) error {
			if keyID == "" || secret.key == nil {
				return errors.New("nexus: WithHMACAuth needs a key id and a secret from NewAPISecret")
			}
			c.t.Signer = &signing.HMAC{KeyID: keyID, Secret: secret.key()}
			c.account = c.ownerFromServer()
			return nil
		})
	}
}

// ownerFromServer asks GET /account for the owner an API key maps to, once;
// a failed lookup is not kept, so the next call asks again.
func (c *Client) ownerFromServer() func(context.Context) (string, error) {
	var (
		mu    sync.Mutex
		owner string
	)
	return func(ctx context.Context) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if owner != "" {
			return owner, nil
		}
		var out struct {
			Owner string `json:"owner"`
		}
		if err := c.t.Get(ctx, "/account", nil, &out); err != nil {
			return "", err
		}
		if out.Owner == "" {
			return "", errors.New("nexus: GET /account did not echo the owner address")
		}
		owner = strings.ToLower(out.Owner)
		return owner, nil
	}
}
