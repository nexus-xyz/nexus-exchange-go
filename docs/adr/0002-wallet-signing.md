# ADR 0002: Wallet signing with go-ethereum

- Status: accepted
- Date: 2026-09-25
- Ticket: ENG-16556 (epic ENG-16552)

## Context

Wallet sign-in, agent registration and agent request signing need four
primitives: Keccak-256, secp256k1 recoverable ECDSA (RFC 6979, low-S, v in
{27, 28}), the EIP-191 `personal_sign` digest, and the EIP-712 digest of
`RegisterAgent{address agent, uint64 expiresAt, uint64 nonce}`.

Most callers of this SDK read market data or trade with an HMAC key and need
none of it. The credential options live on the root `Client`, so whatever the
wallet code imports, every importer of the root package compiles and links.

## Options

| Option | Cost |
| --- | --- |
| **go-ethereum in the root module** (`crypto`, `accounts.TextHash`, `signer/core/apitypes`) | The largest module graph and binary of the options (measured below) |
| go-ethereum in a separate module (`nexus-exchange-go/wallet`) | A second module to version, tag (`wallet/vX.Y.Z`) and release. The credential options could not sit on `Client` without an interface to plug them into |
| go-ethereum behind a build tag | Callers must know to pass `-tags`. A missing tag fails at link time with an unhelpful error, and CI has two builds to test |
| `golang.org/x/crypto/sha3` + `github.com/decred/dcrd/dcrec/secp256k1/v4`, EIP-712 hand-encoded | The smallest (3 modules, +0.78 MB), but we would own the EIP-712 encoding |

The first draft of this PR took the last option. On review, João asked for the
industry-standard option.

## Decision

Use go-ethereum in the root module. There is no separate module and no build
tag:

- `crypto.Keccak256`, `crypto.Sign`, `crypto.SigToPub`, `crypto.ToECDSA` and
  `crypto.GenerateKey` provide the key and signature primitives.
- `accounts.TextHash` provides the EIP-191 digest.
- `apitypes.TypedDataAndHash` provides the EIP-712 digest. It is the general
  typed-data encoder, so no domain or struct encoding is written by hand.

This is the industry-standard choice. go-ethereum is the reference Ethereum
implementation in Go. It is audited, and it is what Go exchange SDKs and
Ethereum tooling almost always sign with. With cgo on, its secp256k1 is
bitcoin-core's libsecp256k1; with cgo off it falls back to the decred
library. Both produce the same RFC 6979 signatures. A reviewer does not have
to trust an encoder written for this SDK.

## What it costs, measured

Measured on 2026-09-25 with a program that only calls `NewClient(Testnet)` and
`Markets` (darwin/amd64, Go 1.26, go-ethereum v1.17.6):

| | Before wallet auth | x/crypto + decred (first draft) | go-ethereum, cgo on | go-ethereum, `CGO_ENABLED=0` |
| --- | --- | --- | --- | --- |
| Binary | 9.58 MB | 10.36 MB (+0.78) | 11.91 MB (+2.33, +24%) | 10.93 MB (+1.35, +14%) |
| Third-party packages linked | 26 | 30 | 71 | |
| Modules in the caller's graph (`go list -m all`) | 81 | | 236 | |

`apitypes` accounts for most of the extra packages. It imports `core/types`,
and that brings in go-ethereum's KZG and BLS12-381 code (gnark-crypto,
go-eth-kzg, blst) even though no EIP-712 path uses them. With cgo on, blst
compiles C. The module still builds and tests with `CGO_ENABLED=0`.

That is the price of not owning the encoding. If a caller shows the size
matters, splitting the wallet code into its own module is still open, since
everything is in `internal/`.

## How it is held correct

`internal/signing/eth_test.go` pins, byte for byte:

- the EIP-191 sign-in signature and the legacy-domain EIP-712 registration
  signature for the Hardhat #0 key. These are the vectors nexus-exchange-rs
  pins against ethers v6, and that nexus-exchange-py (eth-account) and
  nexus-exchange-ts pin in turn;
- the salted-domain `RegisterAgent` digest that the server pins in
  `agent_store::tests::eip712_register_agent_digest_pinned` (alloy);
- the per-network salts published in `x-nexus-networks`;
- the three `agentAuth` vectors from the spec's `x-nexus-test-vectors`.

These vectors passed unchanged across the switch from the hand-written
encoding to go-ethereum. Two independent implementations agreeing on them is
the evidence that both are right.

## Consequences

- Dependabot tracks go-ethereum, which releases often. A go-ethereum bump that
  changes a digest fails the pinned vectors, not a user's registration.
- A new signed struct is a new `apitypes.TypedData` value plus its pinned
  vector. No encoder changes.
