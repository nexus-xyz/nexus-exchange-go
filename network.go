package nexus

import "fmt"

// Network selects which Nexus Exchange deployment a [Client] talks to.
//
// The zero value is not a network. [NewClient] rejects it, so forgetting to
// choose can never send traffic to a default, and in particular can never send
// real-funds intent to testnet or play-funds intent to mainnet.
//
// Every network's REST base is a named entry in one explicit map, copied from
// the spec's x-nexus-networks rest_base values. No host is ever built by
// interpolating the network name: mainnet is deliberately off-pattern
// (api.nexus.xyz, not api.mainnet.nexus.xyz), so a template would resolve for
// every environment that can be tested and be wrong only for real funds.
//
// # One mount, chosen here
//
// The public hosts serve each route both under the edge prefix /v1 and under
// /api/v1. This SDK uses only the first form: base https://<host>/v1 plus the
// spec's bare paths, so an order is sent to /v1/orders (ENG-17186). That lands
// on the indexer's root mounts, which are always served, rather than on the
// /api/v1 nest behind its dual-stack kill switch. The prefix is the single
// constant edgePrefix below; when the dual mount ends, that is the one line to
// change.
//
// The edge strips /v1 before the indexer verifies a request, so a request
// signer must sign the path without the base prefix: /v1/orders is signed as
// /orders.
type Network int

const (
	// Mainnet is real funds: USDX bridged from Ethereum Mainnet, no faucet.
	//
	// Not targetable by this release. Its base, https://api.nexus.xyz/v1, is
	// the published one, but api.nexus.xyz has no DNS record yet (ENG-15183).
	// NewClient(Mainnet) succeeds, and every request through that client is
	// refused locally with [ErrMainnetNotTargetable] before any bytes leave the
	// process, rather than sent to a real-funds host that may not be the one
	// DNS eventually names. The entry exists so that when DNS lands, lifting the
	// refusal is the only change and no code has to guess the host.
	Mainnet Network = iota + 1
	// Testnet is play funds: synthetic USDX from the faucet, no real value.
	// The safe target for integration work and CI.
	Testnet
	// Local is an indexer run on this machine, served directly with no edge
	// and so no /v1 prefix. A developer convenience, not a public network.
	Local
)

// edgePrefix is the path prefix the public edge routes and strips before the
// indexer sees the request. It is part of the base, never of a request path.
const edgePrefix = "/v1"

var restBases = map[Network]string{
	Mainnet: "https://api.nexus.xyz" + edgePrefix,
	Testnet: "https://api.testnet.nexus.xyz" + edgePrefix,
	Local:   "http://localhost:9090",
}

func (n Network) String() string {
	switch n {
	case Mainnet:
		return "mainnet"
	case Testnet:
		return "testnet"
	case Local:
		return "local"
	}
	return fmt.Sprintf("Network(%d)", int(n))
}
