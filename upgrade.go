package nexus

import "github.com/nexus-xyz/nexus-exchange-go/internal/transport"

// VersionError is returned, from any call, for a 426 Upgrade Required
// (api_version_unsupported): the server no longer accepts the spec version
// this SDK is pinned to ([APIVersion]). Upgrading the SDK is the fix; a retry
// fails the same way. It carries the pinned version and, when the response
// named them, the server's current and minimum versions:
//
//	var verr *nexus.VersionError
//	if errors.As(err, &verr) {
//		log.Printf("SDK pins %s; server accepts %s through %s", verr.Pinned, verr.Min, verr.Current)
//	}
//
// It wraps the response's [*APIError], so errors.As reaches that too.
type VersionError = transport.VersionError
