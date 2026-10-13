// The public-surface lister for prepublish-surface (ENG-18798). A module of its own, so it stays
// out of the SDK's module zip on the proxy and out of the SDK's `go list ./...`. Stdlib only.
module github.com/nexus-xyz/nexus-exchange-go/scripts/release_gate/surface

go 1.26.0
