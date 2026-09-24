package nexus

import (
	_ "embed"
	"strings"
)

//go:embed .api-version
var apiVersionFile string

// APIVersion returns the released Exchange API spec tag this SDK is built
// against, for example "v0.8.1". It is embedded from the repository's
// .api-version file at compile time, so it cannot fall behind the pin.
func APIVersion() string { return strings.TrimSpace(apiVersionFile) }
