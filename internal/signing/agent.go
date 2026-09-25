package signing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"crypto/ecdsa"
)

// ErrAgentCannotWithdraw is returned, before any network I/O, for a request an
// agent key may never make. Re-exported by package nexus.
var ErrAgentCannotWithdraw = errors.New("nexus: agent keys trade, they do not withdraw (R2.18): " +
	"an agent-signed request cannot withdraw or move funds off the account, so it was refused locally; " +
	"sign it with the owner wallet instead")

// Agent signs requests with a registered agent key (the spec's agentAuth
// scheme: x-agent, x-timestamp, x-nonce, x-signature). It is safe for
// concurrent use.
type Agent struct {
	key     *ecdsa.PrivateKey
	address string
	// Now is the clock the timestamp is read from; nil means time.Now.
	Now func() time.Time

	mu   sync.Mutex
	last uint64
}

// NewAgent returns a signer for the agent key key.
func NewAgent(key *ecdsa.PrivateKey) *Agent {
	return &Agent{key: key, address: Address(&key.PublicKey)}
}

// Address is the agent's address, sent as x-agent.
func (a *Agent) Address() string { return a.address }

// AgentCanonical is the string an agent signs:
//
//	METHOD \n path \n query \n hex(sha256(body)) \n ts \n nonce
//
// The field order is not the HMAC one: method first, timestamp after the
// body hash.
func AgentCanonical(method, path, query string, body []byte, tsMillis int64, nonce uint64) string {
	sum := sha256.Sum256(body)
	return strings.ToUpper(method) + "\n" + path + "\n" + query + "\n" + hex.EncodeToString(sum[:]) +
		"\n" + strconv.FormatInt(tsMillis, 10) + "\n" + strconv.FormatUint(nonce, 10)
}

// SignAgent returns x-signature for canonical: 0x and the 65-byte signature
// over raw keccak256(canonical), with no EIP-191 prefix.
func SignAgent(key *ecdsa.PrivateKey, canonical string) string {
	return "0x" + hex.EncodeToString(SignHash(key, Keccak256([]byte(canonical))))
}

// Clock is the time the next timestamp will be read from.
func (a *Agent) Clock() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// Sign sets the four agentAuth headers on h, or refuses a request that moves
// funds (R2.18). Every call issues a fresh nonce, max(last+1, now ms): the
// server requires a write's nonce to exceed the last one it accepted from
// this agent, and a counter floored at the clock stays increasing across
// restarts.
func (a *Agent) Sign(_ context.Context, h http.Header, method, path, query string, body []byte) error {
	if MovesFunds(method, path) {
		return ErrAgentCannotWithdraw
	}
	ts := a.Clock().UnixMilli()
	a.mu.Lock()
	nonce := max(a.last+1, uint64(ts))
	a.last = nonce
	a.mu.Unlock()
	h.Set("X-Agent", a.address)
	h.Set("X-Timestamp", strconv.FormatInt(ts, 10))
	h.Set("X-Nonce", strconv.FormatUint(nonce, 10))
	h.Set("X-Signature", SignAgent(a.key, AgentCanonical(method, path, query, body, ts, nonce)))
	return nil
}

// MovesFunds reports whether a request is on a route that takes money off
// the account, which an agent key is structurally barred from: a write to any
// withdrawal route (POST /withdrawals, POST /api/v1/bridge/withdrawals, and
// whatever withdrawal route comes next), or any ordinary-transfer operation
// (/transfers, which refuses x-agent outright). Reading withdrawal history is
// allowed.
func MovesFunds(method, path string) bool {
	if path == "/transfers" || strings.HasPrefix(path, "/transfers/") {
		return true
	}
	return method != http.MethodGet && method != http.MethodHead && strings.Contains(path, "withdraw")
}
