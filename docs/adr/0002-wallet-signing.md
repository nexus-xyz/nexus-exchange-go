# ADR 0002: Wallet signing without go-ethereum

- Status: proposed
- Date: 2026-09-25
- Ticket: ENG-16556 (epic ENG-16552)

## Context

Wallet sign-in, agent registration and agent request signing need four
primitives: Keccak-256, secp256k1 recoverable ECDSA (RFC 6979, low-S, v in
{27, 28}), the EIP-191 `personal_sign` digest, and the EIP-712 digest of one
struct, `RegisterAgent{address agent, uint64 expiresAt, uint64 nonce}`. The
obvious Go source is go-ethereum (`crypto`, `accounts`,
`signer/core/apitypes`), the analogue of the Python SDK's `eth-account`.

Most callers of this SDK read market data or trade with an HMAC key and need
none of it. Whatever the wallet code imports, every importer of the root
package compiles and links, because the credential options live on the root
`Client`.

## Options

Measured on 2026-09-25 with a program that only calls `NewClient(Testnet)`
and `Markets` (darwin/amd64, Go 1.26).

| Option | Modules a caller's go.mod gains | Market-data binary | Cost |
| --- | --- | --- | --- |
| go-ethereum in the root module | go-ethereum plus its graph (its go.mod lists 163 requirements) | not measured | cgo by default for secp256k1, a large module graph, and frequent releases for Dependabot to chase |
| go-ethereum in a separate module (`nexus-exchange-go/wallet`) | none for market data | unchanged | a second module to version, tag (`wallet/vX.Y.Z`) and release, and the credential options could no longer sit on `Client` without an interface to plug them in |
| go-ethereum behind a build tag | none unless the tag is set | unchanged | callers must know to pass `-tags`, and a missing tag fails at link time with an unhelpful error; two builds for CI to test |
| **`golang.org/x/crypto/sha3` + `github.com/decred/dcrd/dcrec/secp256k1/v4`, EIP-712 by hand** | 3 (`secp256k1/v4`, `x/crypto`, `x/sys`) | 9.58 MB to 10.36 MB (+0.78 MB, +8%) | we own about 40 lines of EIP-712 encoding |

These are the same two libraries go-ethereum itself uses for pure-Go
signing, so they are not the less proven choice. Both are pure Go, and
`secp256k1/v4` has one dependency of its own (`blake256`, which is not linked).

EIP-712 by hand is small because the struct is fixed and every field is a
static type: each encodes to one 32-byte word, with no arrays, nested structs
or dynamic types. A general typed-data encoder (`apitypes`) buys nothing here.

## Decision

Use `x/crypto/sha3` and decred's `secp256k1/v4` in the root module, with the
EIP-191 and EIP-712 digests written in `internal/signing/eth.go`. No separate
module and no build tag.

The +0.78 MB is mostly secp256k1's precomputed tables. It is the price of
keeping one module and one `Client`; if a caller shows that it matters, the
split to a separate module is still open, since everything is in `internal/`.

## How it is held correct

`internal/signing/eth_test.go` pins, byte for byte:

- the EIP-191 sign-in signature and the legacy-domain EIP-712 registration
  signature for the Hardhat #0 key, the vectors nexus-exchange-rs pins
  against ethers v6 and nexus-exchange-py (eth-account) and nexus-exchange-ts
  pin in turn;
- the salted-domain `RegisterAgent` digest the server pins in
  `agent_store::tests::eip712_register_agent_digest_pinned` (alloy);
- the per-network salts published in `x-nexus-networks`;
- the three `agentAuth` vectors from the spec's `x-nexus-test-vectors`.

A digest that drifts fails a test, not a user's registration.

## Consequences

- A bug in the hand-written encoding is ours. The pinned vectors are the
  guard, and adding a new signed struct means adding its vector first.
- Dependabot tracks two small modules rather than go-ethereum's graph.
