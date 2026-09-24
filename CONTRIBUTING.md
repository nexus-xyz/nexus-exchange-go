# Contributing

## Development setup

You need Go 1.26 or newer. Before pushing, run what CI runs:

```sh
go build ./...
go vet ./...
go test ./...
gofmt -l .        # must print nothing
go run honnef.co/go/tools/cmd/staticcheck@2026.2.1 ./...
```

Every one of these is a separate CI job and a blocking check.

## Minimum Go version

`go.mod` says `go 1.26.0`. That is the oldest Go release still receiving
upstream security fixes (Go supports the two newest majors; 1.27 is current).
It is well past everything the SDK is expected to lean on: generics (1.18),
`log/slog` (1.21), and range-over-func iterators (1.23, for paginated
endpoints). CI builds and tests on both the go.mod minimum and `stable`.

Raising the minimum is a user-facing change: it needs a `feat` commit (not a
`chore`) so it shows up in the changelog. Lower it only if a real user needs it.

## Package layout

- The root package, `nexus`, is the entire public API. Anything exported
  there is under semver.
- `internal/transport`: HTTP and WebSocket plumbing.
- `internal/signing`: request authentication.
- `internal/models`: types generated from the pinned OpenAPI spec.

`internal/` is enforced by the compiler, so other modules cannot import these
packages. That is the point: generated models change whenever the spec does,
and keeping them internal keeps that churn out of the semver contract. Expose
what users need from the root package with a deliberate type or alias.

## API version

`.api-version` pins the released
[Exchange API spec](https://github.com/nexus-xyz/nexus-exchange-api) tag this
SDK is built against, the same file every sibling SDK carries. The `spec-pin`
CI job fails when it is not a published spec release, and warns (without
failing) when a newer release exists, as the sibling SDKs do (EDR-002). The
monorepo's `api-version-pins.json` does not track SDK pins; see "SDK pins are
separate" in `eng/apps/exchange/api/README.md` there.

The Go value is derived, not retyped: `version.go` embeds `.api-version` with
`//go:embed`, and `nexus.APIVersion()` returns it. We chose `embed` over
`go:generate` because a generated file is a second copy that can be committed
stale, while an embedded file is read by the compiler on every build.
`APIVersion` is a function rather than an exported variable so callers cannot
reassign it.

## Releasing

Releases use [release-please](https://github.com/googleapis/release-please),
like the spec repo and the TypeScript and Python SDKs.

1. `release-please.yml` watches `main` and keeps a standing release PR open,
   built from Conventional Commit subjects (`feat:`, `fix:`, `feat!:`). It
   updates `CHANGELOG.md` and `.release-please-manifest.json`. Nothing ships
   while it is open. If its CI shows no checks, approve the workflow run from
   the PR (GitHub gates runs started by `github-actions[bot]`).
2. Merging the release PR is the release. release-please tags the merge commit
   `vX.Y.Z` and publishes the GitHub release. For a Go module the tag is the
   release; there is no registry upload.

Pre-1.0 policy: `feat!` bumps the minor version and `feat`/`fix` bump the
patch, so the computed version stays below 1.0.0. The first release should be
`v0.1.0`; put `Release-As: 0.1.0` in the body of the commit that should cut it.

Do not push tags by hand.

### A bad tag is fixed with a new tag

Once a tag is pushed, `proxy.golang.org` and `sum.golang.org` may already have
cached it, and they serve that content forever. Deleting or moving the tag does
not remove it from the proxy; it only makes the checksum database disagree with
the repository, which breaks builds for anyone who fetched the original.

So never delete, move, or re-push a tag. Fix a bad release by releasing a new
version, and add a `retract` directive for the bad one to `go.mod` in that
release (`retract v0.1.1 // reason`) so `go get` steers users away from it.
There is no other fix.
