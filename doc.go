// Package nexus is the Go client for the Nexus Exchange API.
//
// This package is the whole public surface. Transport, request signing and the
// models generated from the OpenAPI spec live under internal/, which the Go
// compiler refuses to let other modules import, so none of them fall under
// this module's semver promise by accident.
package nexus
