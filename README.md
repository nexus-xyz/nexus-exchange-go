# nexus-exchange-go

The official Go SDK for the Nexus Exchange API: REST market data and trading,
the market-data and account WebSockets, and every credential the API accepts
(API keys, wallet sign-in, agent keys).

The package documentation on
[pkg.go.dev](https://pkg.go.dev/github.com/nexus-xyz/nexus-exchange-go) is the
reference. This README is the short version.

No release is tagged yet, so `@latest` below resolves to the tip of `main`.

## Install

```sh
go get github.com/nexus-xyz/nexus-exchange-go@latest
```

Go 1.26 or newer. The package name is `nexus`:

```go
import nexus "github.com/nexus-xyz/nexus-exchange-go"

c, err := nexus.NewClient(nexus.Testnet) // public data needs no credential
```

## Examples

Three runnable programs in [`examples/`](examples), each built and vetted by CI:

| Example | What it shows | Needs |
| --- | --- | --- |
| [`marketdata`](examples/marketdata/main.go) | A ticker over REST, then book snapshots from the public WebSocket | Nothing |
| [`placecancel`](examples/placecancel/main.go) | Place one post-only limit order, cancel it | A testnet API key |
| [`bot`](examples/bot/main.go) | A quoting loop: streams the book, keeps a bid and an ask outside the touch, turns on cancel-on-disconnect, and cancels everything on shutdown | A testnet API key and testnet collateral |

```sh
go run ./examples/marketdata
NEXUS_TESTNET_KEY_ID=... NEXUS_TESTNET_KEY_SECRET=... go run ./examples/bot -duration 1m
```

**Where examples live.** The programs here answer "how do I call this?": one
file per surface, no application around it. Whole applications built on the
Exchange ("what does a real app look like?") live in
[`nexus-xyz/nexus-exchange-examples`](https://github.com/nexus-xyz/nexus-exchange-examples),
not here.

## Networks

A client is bound to one network, and there is no default: you name it.

| Network | Funds | Status |
| --- | --- | --- |
| `nexus.Testnet` | Play funds (synthetic USDX from the faucet) | The target for development and CI |
| `nexus.Mainnet` | Real funds | Built, but every request is refused locally with `ErrMainnetNotTargetable` until its host resolves (ENG-15183) |
| `nexus.Local` | Whatever your local stack holds | An indexer on `localhost:9090`, no edge |

Every host is a named entry in one map, never a template: mainnet is
deliberately off the `api.{network}.nexus.xyz` pattern. Credentials are bound
to the network that issued them; a testnet key is refused on mainnet.

## Credentials

A client holds at most one credential.

- **API key (HMAC), the usual choice for a bot.**
  `nexus.WithHMACAuth(keyID, secret)`, with `secret` from
  `nexus.NewAPISecret(issuedHex)`. The secret is issued as hex text and
  must be hex-decoded before signing; `NewAPISecret` does that, and is the only
  way to build one. Signing with the hex text gets a `401` on every call, and a
  `401` is opaque by design, so nothing tells you why.
- **Wallet sign-in and sessions.** `nexus.WithWallet(key)` signs in with the
  owner wallet (EIP-191) and keeps the session fresh. Or sign in once and
  pass the session to `nexus.WithSession`; it is refused locally once it
  expires. A session is full authority over the account, and it can mint API
  keys.
- **Agent keys.** The wallet registers an agent key (EIP-712), and
  `nexus.WithAgent(agent)` trades as the wallet's account. An
  agent can never withdraw (R2.18): the server refuses it, and the SDK refuses
  it locally first with `ErrAgentCannotWithdraw`.

Keys, secrets and sessions cannot be printed: every `fmt` verb and JSON
encoding shows a placeholder or the address.

**Who am I.** Every client can say which account it acts for, whatever the
credential. Wallets, sessions and agents know it locally. An API
key does not carry its owner, so for HMAC the SDK asks the server once, through
`GET /account/deposit-target`. That route is a stand-in until `GET /whoami`
(ENG-17767) ships in a spec release.

## Money is never a float64

Every price, quantity, balance and PnL is a `nexus.Decimal`, which holds the
decimal string exactly as served. The API serves money as strings so that no
client does float arithmetic on it; converting to `float64` on the way in
reintroduces the rounding the contract was shaped to avoid. Build values with
`nexus.ParseDecimal("1.50")`, and do arithmetic with `Decimal.Rat` (`math/big`)
or your own decimal library.

## Behaviour a bot should know

- **Orders are sent once.** Any request that can change state is never
  retried, so a timeout means "unknown": read the order back before
  resubmitting. GETs are retried a few times.
- **Cancels are never delayed.** The client paces reads and submissions on the
  budgets the server reports, weighted per operation from the pinned spec, but
  a cancel goes out immediately.
- **Cancel-on-disconnect needs the account socket.** Once enabled
  (`Account.SetCancelOnDisconnect`), the exchange cancels your resting orders
  when your last account WebSocket (`Client.Subscribe`) drops and stays down
  past the grace window. REST traffic and the public market stream do not
  count. Check `Active` on the result, not just `Enabled`.
- **Preview costs an order.** `POST /orders/preview` is billed as a trading
  action, so previewing before every order halves your effective placement
  rate. `Client.PreviewOrder` wraps it; nothing in the SDK calls it for you.

## Versions and pinning

Two versions are in play:

- **The SDK's own**, a semver tag on this module. Pin it in your `go.mod` like
  any dependency. Releases are cut by release-please; a bad release is fixed by
  a newer one, never by moving a tag.
- **The Exchange API spec release it is built against**, pinned in
  [`.api-version`](.api-version) and returned by `nexus.APIVersion()`, which
  embeds that file at compile time. Every request sends it in
  `X-Nexus-Api-Version`. A server that no longer accepts it answers `426`,
  returned as a `*nexus.VersionError`; the fix is upgrading the SDK. CI fails if
  the pin is not a published spec release, and warns when a newer one exists.

The docs never type a spec version number (a test enforces it): they say "the
pinned spec", which always means `APIVersion()`.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

Licensed under either of [Apache License, Version 2.0](LICENSE-APACHE) or
[MIT license](LICENSE-MIT) at your option.
