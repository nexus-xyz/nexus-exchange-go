package nexus

import (
	"context"
	"errors"
	"net/http"
	"net/url"
)

// WithAdminSecret authenticates every request with the operator's admin
// secret (the adminAuth scheme, a bearer token), for the tier operations
// [Client.FetchTiers], [Client.SetTier] and [Client.DeleteTier]. It is an
// operator credential, not an account one: the client acts for no account,
// so [Client.AccountAddress] returns [ErrNoCredential].
//
// NewClient returns an error if secret is empty.
func WithAdminSecret(secret string) Option {
	return func(cfg *config) {
		cfg.creds = append(cfg.creds, func(c *Client) error {
			if secret == "" {
				return errors.New("nexus: WithAdminSecret needs the admin secret")
			}
			c.t.Signer = adminBearer(func() string { return secret })
			return nil
		})
	}
}

// adminBearer holds the secret in a func, like [APISecret], so printing a
// client never prints it.
type adminBearer func() string

func (b adminBearer) Sign(_ context.Context, h http.Header, _, _, _ string, _ []byte) error {
	h.Set("Authorization", "Bearer "+b())
	return nil
}

// TierEntry is one account's rate-limit tier override. The pinned spec
// ([APIVersion]) publishes only an example for the tier operations, so the
// type is written here from it.
type TierEntry struct {
	Address string `json:"address"`
	Tier    string `json:"tier"`
}

// FetchTiers lists the rate-limit tier overrides (GET /admin/tiers). It needs
// [WithAdminSecret].
func (c *Client) FetchTiers(ctx context.Context) ([]TierEntry, error) {
	return getJSON[[]TierEntry](ctx, c, "/admin/tiers", nil)
}

// SetTier sets the account at address to tier, such as "MarketMaker" (PUT
// /admin/tiers), and returns the override. It needs [WithAdminSecret], and is
// sent once and never retried.
func (c *Client) SetTier(ctx context.Context, address, tier string) (*TierEntry, error) {
	var out TierEntry
	if err := c.t.Send(ctx, http.MethodPut, "/admin/tiers", TierEntry{address, tier}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteTier removes the account's tier override, returning it to the default
// tier (DELETE /admin/tiers/{address}). It needs [WithAdminSecret].
func (c *Client) DeleteTier(ctx context.Context, address string) error {
	return c.t.Send(ctx, http.MethodDelete, "/admin/tiers/"+url.PathEscape(address), nil, nil)
}
