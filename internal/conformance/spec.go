// Package conformance runs SDK methods against a live API and joins what comes
// back against the pinned spec. It is the model of the monorepo's
// ccxt-conformance harness, and it keeps that harness's two lessons.
//
// A call is not a measurement. A method that answered an empty list is a
// correct answer that proves nothing about shape, so the report counts called
// and measured separately and an empty answer is never a pass.
//
// Severity is a join, not a judgement. A null is a defect only where the
// contract declares the field non-nullable: a field declared ["number","null"]
// that comes back null is the contract working, and reports as info.
package conformance

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Operation is one operation of the spec, on its root mount.
type Operation struct {
	ID, Method, Path string
	// Public is true when the operation needs no credential: it declares no
	// security, or an empty requirement among its alternatives.
	Public bool
	// Schema is the declared 200 or 201 JSON response schema; nil when the
	// operation declares none, and then there is nothing to measure.
	Schema any
}

// Spec is the part of an OpenAPI document the lane joins against.
type Spec struct {
	Version string
	Ops     map[string]Operation // by operationId
	schemas map[string]any
}

// ParseSpec reads an OpenAPI 3.1 document.
func ParseSpec(data []byte) (*Spec, error) {
	var doc struct {
		Info       struct{ Version string }
		Security   []map[string]any
		Paths      map[string]map[string]json.RawMessage
		Components struct{ Schemas map[string]any }
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("conformance: parse spec: %w", err)
	}
	s := &Spec{Version: doc.Info.Version, Ops: map[string]Operation{}, schemas: doc.Components.Schemas}
	for path, item := range doc.Paths {
		// The SDK sends the bare paths only; /api/v1 is the dual mount.
		if strings.HasPrefix(path, "/api/v1/") {
			continue
		}
		for method, raw := range item {
			var op struct {
				OperationID string `json:"operationId"`
				Security    *[]map[string]any
				Responses   map[string]struct {
					Content map[string]struct{ Schema any }
				}
			}
			if json.Unmarshal(raw, &op) != nil || op.OperationID == "" {
				continue // "parameters", "summary" and the like
			}
			security := doc.Security
			if op.Security != nil {
				security = *op.Security
			}
			public := len(security) == 0
			for _, req := range security {
				public = public || len(req) == 0
			}
			var schema any
			for _, code := range []string{"200", "201"} {
				if r, ok := op.Responses[code]; ok {
					schema = r.Content["application/json"].Schema
					break
				}
			}
			s.Ops[op.OperationID] = Operation{op.OperationID, strings.ToUpper(method), path, public, schema}
		}
	}
	return s, nil
}

// routes reports whether a request for method and path (no query) is one for
// op, reading a {param} segment of the template as any one segment.
func (op Operation) routes(method, path string) bool {
	if method != op.Method {
		return false
	}
	want, got := strings.Split(op.Path, "/"), strings.Split(path, "/")
	if len(want) != len(got) {
		return false
	}
	for i := range want {
		if want[i] != got[i] && !(strings.HasPrefix(want[i], "{") && got[i] != "") {
			return false
		}
	}
	return true
}
