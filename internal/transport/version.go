package transport

import (
	"encoding/json"
	"net/http"
	"strings"
)

// VersionError is a 426 Upgrade Required: the server no longer accepts the
// spec version this SDK is pinned to (code api_version_unsupported). It is
// the one version failure a caller can act on, by upgrading the SDK.
//
// It wraps the response's *APIError, so errors.As reaches either.
type VersionError struct {
	// Pinned is the version this SDK sent in X-Nexus-Api-Version.
	Pinned string
	// Current is the newest version the server serves, and Min the oldest it
	// still accepts, exactly as served (the indexer sends them without the
	// leading "v"). Either is empty when the response did not carry it.
	Current string
	Min     string
	// SpecURL is where the server says the current spec is; empty if absent.
	SpecURL string

	Err *APIError
}

func (e *VersionError) Error() string {
	s := "nexus: the server no longer supports API version " + e.Pinned + " (HTTP 426"
	if e.Err.Code != "" {
		s += " " + e.Err.Code
	}
	s += ")"
	if e.Min != "" {
		s += "; oldest accepted " + e.Min
	}
	if e.Current != "" {
		s += "; current " + e.Current
	}
	return s + "; upgrade nexus-exchange-go"
}

func (e *VersionError) Unwrap() error { return e.Err }

// newVersionError reads the versions from the indexer's 426 body
// (min_version, current_version, spec_url, which decodeError leaves in
// Details) and, failing that, the X-Nexus-Api-Min-Version header.
func newVersionError(e *APIError, pinned string, h http.Header) *VersionError {
	str := func(key string) (s string) {
		if raw, ok := e.Details[key]; ok {
			_ = json.Unmarshal(raw, &s)
		}
		return s
	}
	v := &VersionError{
		Pinned:  pinned,
		Current: str("current_version"),
		Min:     str("min_version"),
		SpecURL: str("spec_url"),
		Err:     e,
	}
	if v.Min == "" {
		v.Min = strings.TrimSpace(h.Get("X-Nexus-Api-Min-Version"))
	}
	return v
}
