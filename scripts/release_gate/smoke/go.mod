// The pre-publish smoke's consumer (ENG-18798). A module of its own, so it stays out of the SDK's
// module zip on the proxy and out of the SDK's `go list ./...`. Stdlib and the SDK only.
//
// It names no version of the SDK: go.work beside it `use`s the repo root in its place, the way a
// replace would, and smoke.sh builds it from `git archive HEAD`, so that root is a clean copy of
// the committed tree. The SDK's own go.mod and go.sum then resolve the SDK's dependencies, as they
// do for a consumer, so a dependency bump in the SDK can never leave this module stale.
module github.com/nexus-xyz/nexus-exchange-go/scripts/release_gate/smoke

go 1.26.0
