// Package nexus is the Go client for the Nexus Exchange API.
//
// This package is the whole public surface. Transport, request signing and the
// models generated from the OpenAPI spec live under internal/, which the Go
// compiler refuses to let other modules import, so none of them fall under
// this module's semver promise by accident.
//
// # Money and time
//
// Every price, quantity, balance and PnL is a [Decimal], never a float64 (see
// its documentation for why). Counts, basis points, sequence numbers and
// timestamps are integers. Timestamps are epoch milliseconds in int64 fields
// (served as *_at_ms or *_ms). CCXT-shaped structures carry both the numeric
// timestamp (int64 epoch ms) and the ISO-8601 datetime string exactly as
// served; the SDK keeps both rather than collapsing them into one time.Time,
// so it never decides for the caller which one is authoritative.
package nexus
