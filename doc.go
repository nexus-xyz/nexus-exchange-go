// Package nexus is the official Go client for the Nexus Exchange API: REST
// market data and trading, the two WebSocket streams, and every credential
// the API accepts.
//
//	go get github.com/nexus-xyz/nexus-exchange-go
//
// Public market data needs no credential:
//
//	c, err := nexus.NewClient(nexus.Testnet)
//	if err != nil {
//		return err
//	}
//	t, err := c.FetchTicker(ctx, "BTC-USDX-PERP")
//
// Trading needs one, usually an API key:
//
//	secret, err := nexus.NewAPISecret(os.Getenv("NEXUS_TESTNET_KEY_SECRET")) // the hex text, as issued
//	if err != nil {
//		return err
//	}
//	c, err := nexus.NewClient(nexus.Testnet, nexus.WithHMACAuth(os.Getenv("NEXUS_TESTNET_KEY_ID"), secret))
//	if err != nil {
//		return err
//	}
//	price, _ := nexus.ParseDecimal("1000")
//	size, _ := nexus.ParseDecimal("0.001")
//	placed, err := c.CreateOrder(ctx, nexus.OrderRequest{
//		MarketId:    "BTC-USDX-PERP",
//		Side:        nexus.Buy,
//		OrderType:   nexus.OrderTypeLimit,
//		TimeInForce: nexus.PostOnly,
//		Price:       &price,
//		Quantity:    size,
//	})
//
// Runnable programs live in the module's examples directory: market data,
// place and cancel, and a quoting bot that streams, turns on
// cancel-on-disconnect and shuts down cleanly. Whole applications built on the
// Exchange live in github.com/nexus-xyz/nexus-exchange-examples.
//
// This package is the whole public surface. Transport, request signing and the
// models generated from the OpenAPI spec live under internal/, which the Go
// compiler refuses to let other modules import, so none of them fall under
// this module's semver promise by accident.
//
// # Networks
//
// A [Client] is bound to one [Network], chosen explicitly, because there is no
// default: [Testnet] (play funds, the target for development and CI),
// [Mainnet] (real funds; built, but refused locally with
// [ErrMainnetNotTargetable] until its host resolves) or [Local] (an indexer on
// this machine). Credentials are bound to a network too: a key, session or
// agent from testnet is refused on mainnet, and the other way round.
//
// # Credentials
//
// A [Client] holds at most one credential, and every one of them can say which
// account it acts for ([Client.AccountAddress]):
//
//   - [WithHMACAuth]: an API key. The usual choice for a bot. Build its secret
//     with [NewAPISecret], which hex-decodes the issued text; signing with the
//     text itself gets an opaque 401 on every call.
//   - [WithWallet]: the owner wallet's key. The client signs in (EIP-191) and
//     keeps its session fresh. Full authority over the account.
//   - [WithSession]: a session from [Client.Login], used until it expires and
//     then refused locally with [ErrSessionExpired]. A session can mint API
//     keys with [Client.CreateAPIKey].
//   - [WithAgent]: an agent key the wallet registered with
//     [Client.RegisterAgent] (EIP-712). It trades for the wallet's account and
//     can never withdraw ([ErrAgentCannotWithdraw], R2.18).
//
// An API key does not carry its owner, so for [WithHMACAuth] the first
// AccountAddress call asks the server, through GET /account/deposit-target.
// That route is a stand-in until GET /whoami (ENG-17767) ships in a spec
// release.
//
// # Money and time
//
// Every price, quantity, balance and PnL is a [Decimal], never a float64 (see
// its documentation for why). Counts, basis points, sequence numbers and
// timestamps are integers. Timestamps are epoch milliseconds in int64 fields
// (served as *_at_ms or *_ms). CCXT-shaped structures carry both the numeric
// timestamp (int64 epoch ms) and the ISO-8601 datetime string exactly as
// served; the SDK keeps both rather than collapsing them into one time.Time,
// so it never decides for the caller which one is authoritative.
//
// # Orders, retries and rate limits
//
// A request that can change state is sent exactly once and never retried; on
// a timeout, read the order back before resubmitting. GETs are retried a few
// times. The client paces itself on the budgets the server reports, charging
// each call its weight from the pinned spec, and never delays a cancel (see
// [Client] and [Client.CancelOrder]). A 429 is an [*APIError] matching
// [ErrRateLimited] and carrying Retry-After. [Client.PreviewOrder] is billed as
// an order, not a read: see [Client.CreateOrder].
//
// # Streaming
//
// Two sockets, two jobs. [Client.MarketStream] is the keyless market-data
// socket (book snapshots, trades, market status). [Client.Subscribe] is the
// authenticated account socket (orders, fills, positions, balances), with
// per-channel sequence tracking and resume. Both reconnect on their own and
// report it as events.
//
// Cancel-on-disconnect ([Account.SetCancelOnDisconnect]) is the safety net
// for a bot: when the account socket drops and stays down past a grace window,
// the exchange cancels every resting order. It keys on [Client.Subscribe]
// only, so a bot that wants it holds a [Subscription] open.
//
// # Versions
//
// Two versions are in play. One is the module's own, a semver tag you pin in
// go.mod like any Go dependency. The other is the Exchange API spec release
// the SDK is built against, which the repository pins in its .api-version
// file and [APIVersion] returns. Every request states that spec version, and a
// server that no longer accepts it answers with a [*VersionError]: upgrade the
// SDK. Where the documentation here says "the pinned spec", it means that
// release.
package nexus
