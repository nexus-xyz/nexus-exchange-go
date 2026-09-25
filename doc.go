// Package nexus is the Go client for the Nexus Exchange API.
//
// This package is the whole public surface. Transport, request signing and the
// models generated from the OpenAPI spec live under internal/, which the Go
// compiler refuses to let other modules import, so none of them fall under
// this module's semver promise by accident.
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
// # Credentials
//
// A [Client] holds at most one credential, and every one of them can say which
// account it acts for ([Client.AccountAddress]):
//
//   - [WithHMACAuth]: an API key. The usual choice for a bot.
//   - [WithWallet]: the owner wallet's key. The client signs in (EIP-191) and
//     keeps its session fresh. Full authority over the account.
//   - [WithSession]: a session from [Client.SignIn], used until it expires and
//     then refused locally with [ErrSessionExpired].
//   - [WithAgent]: an agent key the wallet registered with
//     [Client.RegisterAgent] (EIP-712). It trades for the wallet's account and
//     can never withdraw ([ErrAgentCannotWithdraw], R2.18).
//
// Credentials are bound to one network. A key, session or agent from testnet
// is refused on mainnet, and the other way round.
package nexus
