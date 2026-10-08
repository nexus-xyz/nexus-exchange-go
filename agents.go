package nexus

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nexus-xyz/nexus-exchange-go/internal/models"
	"github.com/nexus-xyz/nexus-exchange-go/internal/signing"
	"github.com/nexus-xyz/nexus-exchange-go/internal/transport"
)

// registerChainID is the agent-management domain chainId (RegisterAgent,
// RevokeAgentKey). The server verifies against 20056 (0x4E58, "NX") when the
// request names no chain; a registration never names one, and a revocation
// names this one in x-wallet-chain-id. It is a domain separator only, not a
// chain anything runs on; the network is bound by the domain salt instead
// (ENG-11924).
const registerChainID = 20056

// defaultAgentTTL is used when RegisterAgentOptions.ExpiresAt is zero. The
// server accepts [now+1d, now+90d].
const defaultAgentTTL = 30 * 24 * time.Hour

// AgentInfo is one registered agent from [Client.FetchAgents]. Its fields are
// camelCase on the wire (expiresAt, registeredAt), unlike its neighbours.
type AgentInfo = models.AgentInfo

// ErrAgentCannotWithdraw is returned, before any network I/O, when a client
// built [WithAgent] is asked to withdraw or otherwise move funds off the
// account. Agent keys trade; they do not withdraw (R2.18). The server refuses
// the same request with an opaque 401 or 403, so the SDK says why up front.
var ErrAgentCannotWithdraw = signing.ErrAgentCannotWithdraw

// Agent is a registered agent key and the account it trades for. Get one
// from [Client.RegisterAgent], or rebuild a saved one with [NewAgent], and
// pass it to [WithAgent].
//
// Share one Agent between clients rather than building two from the same
// key: it issues the request nonces, and two issuers of one key collide.
// Even with one issuer, two writes in flight at once can arrive out of nonce
// order and the later-numbered one wins; the other gets an opaque 401
// (ENG-17010). Keep one write in flight per agent key, or register one agent
// per concurrent writer.
type Agent struct {
	key       *PrivateKey
	owner     string
	expiresAt time.Time
	signer    *signing.Agent
}

// NewAgent rebuilds an agent registered earlier: key is the agent key and
// owner the wallet address it was registered to. The SDK cannot check the
// registration without a request; a wrong owner shows up as a 401.
func NewAgent(key *PrivateKey, owner string) (*Agent, error) {
	if key == nil {
		return nil, errors.New("nexus: NewAgent needs the agent key")
	}
	if _, err := signing.ParseAddress(owner); err != nil {
		return nil, err
	}
	return &Agent{key: key, owner: strings.ToLower(owner), signer: signing.NewAgent(key.key())}, nil
}

// Address is the agent key's own address, the x-agent header.
func (a *Agent) Address() string { return a.key.Address() }

// Owner is the wallet the agent trades for: the account address.
func (a *Agent) Owner() string { return a.owner }

// ExpiresAt is the registration's expiry, or zero for an agent rebuilt with
// [NewAgent].
func (a *Agent) ExpiresAt() time.Time { return a.expiresAt }

// RegisterAgentOptions tunes [Client.RegisterAgent]. The zero value is valid.
type RegisterAgentOptions struct {
	// ExpiresAt is when the agent lapses; zero means 30 days from now. The
	// server accepts 1 to 90 days ahead.
	ExpiresAt time.Time
	// Label names the agent in [Client.FetchAgents]. It is not signed.
	Label string
}

// RegisterAgent delegates trading from wallet to agent (POST
// /agents/register). wallet signs an EIP-712 RegisterAgent{agent,
// expiresAt, nonce} whose domain is salted with this client's network, so the
// registration verifies only there. No session or API key is needed; c may
// have none.
//
// The returned Agent trades as wallet's account through [WithAgent], and is
// refused locally on every withdrawal route (R2.18).
func (c *Client) RegisterAgent(ctx context.Context, wallet, agent *PrivateKey, opts RegisterAgentOptions) (*Agent, error) {
	if wallet == nil || agent == nil {
		return nil, errors.New("nexus: RegisterAgent needs a wallet key and an agent key")
	}
	now := c.now()
	expires := opts.ExpiresAt
	if expires.IsZero() {
		expires = now.Add(defaultAgentTTL)
	}
	expMs, nonce := expires.UnixMilli(), now.UnixMilli()
	agentAddr, _ := signing.ParseAddress(agent.Address()) // derived, so always valid
	digest, err := signing.RegisterAgentDigest(registerChainID, signing.NetworkSalt(c.network.String()),
		agentAddr, uint64(expMs), uint64(nonce))
	if err != nil {
		return nil, fmt.Errorf("nexus: RegisterAgent typed data: %w", err)
	}
	body := models.AgentRegistrationRequest{
		Wallet:    wallet.Address(),
		Agent:     agent.Address(),
		ExpiresAt: &expMs,
		Nonce:     nonce,
		Signature: "0x" + hex.EncodeToString(signing.SignHash(wallet.key(), digest)),
	}
	if opts.Label != "" {
		body.Label = &opts.Label
	}
	// Hand-decoded: the pinned spec has an example for this response but no schema.
	var out struct {
		ExpiresAt int64 `json:"expires_at"`
	}
	if err := c.pub.Send(ctx, http.MethodPost, "/agents/register", body, &out); err != nil {
		return nil, err
	}
	// NewAgent cannot fail here: agent is non-nil (checked above) and
	// wallet.Address() is derived from a key, so it is always a valid address.
	a, _ := NewAgent(agent, wallet.Address())
	a.expiresAt = time.UnixMilli(expMs)
	if out.ExpiresAt != 0 {
		a.expiresAt = time.UnixMilli(out.ExpiresAt)
	}
	return a, nil
}

// FetchAgents lists the account's unexpired agents (GET /agents). The server
// refuses an agent-signed request here (AGENT_KEY_FORBIDDEN); use the API key
// or wallet session.
func (c *Client) FetchAgents(ctx context.Context) ([]AgentInfo, error) {
	var out []AgentInfo
	if err := c.t.Get(ctx, "/agents", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// RevokeAgent revokes an agent at once (DELETE /agents/{address}); requests
// it signed are refused from then on. wallet, the owner the agent was
// registered to, signs an EIP-712 RevokeAgentKey{account, agent, nonce} under
// the same network-salted domain as [Client.RegisterAgent]. That signature is
// the request's only credential: the server accepts no API key, session or
// agent here, so c may have none, or only an agent.
//
// The nonce is the current Unix time in milliseconds. The server accepts it
// from 5 minutes behind its clock to 60 seconds ahead, and only once: it must
// be greater than the last nonce wallet used to rename or revoke an agent, so
// a second revocation by the same wallet in the same millisecond is refused.
//
// The address cannot be registered again until its original expiry passes;
// register a new agent key instead.
func (c *Client) RevokeAgent(ctx context.Context, wallet *PrivateKey, address string) error {
	if wallet == nil {
		return errors.New("nexus: RevokeAgent needs the wallet key the agent was registered to")
	}
	agent, err := signing.ParseAddress(address)
	if err != nil {
		return err
	}
	account, _ := signing.ParseAddress(wallet.Address()) // derived, so always valid
	nonce := uint64(c.now().UnixMilli())
	digest, err := signing.RevokeAgentKeyDigest(registerChainID, signing.NetworkSalt(c.network.String()),
		account, agent, nonce)
	if err != nil {
		return fmt.Errorf("nexus: RevokeAgent typed data: %w", err)
	}
	h := http.Header{}
	h.Set("X-Wallet-Account", wallet.Address())
	h.Set("X-Wallet-Nonce", strconv.FormatUint(nonce, 10))
	h.Set("X-Wallet-Signature", "0x"+hex.EncodeToString(signing.SignHash(wallet.key(), digest)))
	h.Set("X-Wallet-Chain-Id", strconv.Itoa(registerChainID))
	return c.pub.Send(transport.WithHeaders(ctx, h), http.MethodDelete, "/agents/"+url.PathEscape(address), nil, nil)
}
