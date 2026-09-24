// Package models holds the wire types for the Exchange API.
//
// models.gen.go is generated from the OpenAPI spec at the tag pinned in
// .api-version, for the v1 operations listed in oapi-codegen.yaml. Regenerate
// it with `go generate ./...`; never edit it by hand. decimal.go is the one
// hand-written file: the Decimal type every money field uses, kept here so the
// generated code can refer to it without an import cycle.
package models

//go:generate sh -c "go tool -modfile=../../tools.mod oapi-codegen -config oapi-codegen.yaml https://github.com/nexus-xyz/nexus-exchange-api/releases/download/$(tr -d '[:space:]' < ../../.api-version)/openapi.json"
