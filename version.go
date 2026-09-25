package nexus

import (
	_ "embed"
	"strings"
)

//go:embed .api-version
var apiVersionFile string

// APIVersion returns the released Exchange API spec tag this SDK is built
// against, in the form "vX.Y.Z". It is embedded from the repository's
// .api-version file at compile time, so it cannot fall behind the pin. Doc
// comments in this module that say "the pinned spec" mean this release, and
// never name it by number: a typed version goes stale, this one cannot.
func APIVersion() string { return strings.TrimSpace(apiVersionFile) }
