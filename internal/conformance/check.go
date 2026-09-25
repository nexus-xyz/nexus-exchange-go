package conformance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Severity ranks a finding.
type Severity int

const (
	// Info is the contract being honoured, or a note on what went unmeasured.
	Info Severity = iota
	// Warn is served but not declared: the spec is narrower than the wire.
	Warn
	// Error is a broken promise: a value present and wrong, a required field
	// absent, or a null where the contract declares none. It fails the run.
	Error
)

func (s Severity) String() string { return [...]string{"info", "warn", "error"}[s] }

// Finding is one deviation, counted over every item it occurred on.
type Finding struct {
	Severity Severity
	Path     string // JSON path; [] is any array element, {} any map value
	Msg      string
	N        int
}

// Check joins body, a 2xx response of op, against the schema op declares. It
// returns how many items there were to measure (the elements of a list or a
// map, else 1 for a non-empty object or a scalar) and what it found.
func (s *Spec) Check(op Operation, body []byte) (int, []Finding) {
	c := &checker{schemas: s.schemas}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		c.add(Error, "", "response is not JSON: "+err.Error())
		return 0, c.out
	}
	c.walk("", op.Schema, v)
	items := 1
	switch v := v.(type) {
	case []any:
		items = len(v)
	case map[string]any:
		items = len(v)
		if m := c.flatten(op.Schema, 0); m != nil && m["properties"] != nil && len(v) > 0 {
			items = 1 // an object, not a map of items
		}
	case nil:
		items = 0
	}
	slices.SortStableFunc(c.out, func(a, b Finding) int {
		if a.Severity != b.Severity {
			return int(b.Severity - a.Severity)
		}
		return strings.Compare(a.Path, b.Path)
	})
	return items, c.out
}

type checker struct {
	schemas map[string]any
	out     []Finding
}

func (c *checker) add(sev Severity, path, msg string) {
	for i := range c.out {
		if f := &c.out[i]; f.Severity == sev && f.Path == path && f.Msg == msg {
			f.N++
			return
		}
	}
	c.out = append(c.out, Finding{sev, path, msg, 1})
}

func (c *checker) count(sev Severity) (n int) {
	for _, f := range c.out {
		if f.Severity == sev {
			n += f.N
		}
	}
	return n
}

// flatten follows $ref and merges allOf, so the result is one schema object.
func (c *checker) flatten(s any, depth int) map[string]any {
	m, _ := s.(map[string]any)
	if m == nil || depth > 16 {
		return nil
	}
	if ref, ok := m["$ref"].(string); ok {
		return c.flatten(c.schemas[strings.TrimPrefix(ref, "#/components/schemas/")], depth+1)
	}
	all, ok := m["allOf"].([]any)
	if !ok {
		return m
	}
	out := map[string]any{}
	props := map[string]any{}
	var required []any
	parts := []map[string]any{m}
	for _, b := range all {
		parts = append(parts, c.flatten(b, depth+1))
	}
	for _, f := range parts {
		for k, v := range f {
			if _, set := out[k]; !set && k != "allOf" {
				out[k] = v
			}
		}
		if p, ok := f["properties"].(map[string]any); ok {
			for k, v := range p {
				props[k] = v
			}
		}
		r, _ := f["required"].([]any)
		required = append(required, r...)
	}
	if len(props) > 0 {
		out["properties"] = props
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func types(m map[string]any) map[string]bool {
	out := map[string]bool{}
	switch t := m["type"].(type) {
	case string:
		out[t] = true
	case []any:
		for _, x := range t {
			if s, ok := x.(string); ok {
				out[s] = true
			}
		}
	}
	return out
}

func branches(m map[string]any) []any {
	if b, ok := m["oneOf"].([]any); ok {
		return b
	}
	b, _ := m["anyOf"].([]any)
	return b
}

func (c *checker) nullable(m map[string]any) bool {
	if types(m)["null"] {
		return true
	}
	for _, b := range branches(m) {
		if f := c.flatten(b, 0); f != nil && c.nullable(f) {
			return true
		}
	}
	return false
}

// kind is the JSON type of v, with integer apart from number.
func kind(v any) string {
	switch v := v.(type) {
	case string:
		return "string"
	case json.Number:
		if strings.ContainsAny(string(v), ".eE") {
			return "number"
		}
		return "integer"
	case bool:
		return "boolean"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	}
	return "null"
}

func join(path, key string) string {
	if path == "" || key == "[]" {
		return path + key
	}
	return path + "." + key
}

func (c *checker) walk(path string, s any, v any) {
	m := c.flatten(s, 0)
	if len(m) == 0 {
		return // no schema, nothing promised
	}
	if v == nil {
		if c.nullable(m) {
			c.add(Info, path, "null, which the contract permits")
		} else {
			c.add(Error, path, "null, but the contract declares it non-nullable")
		}
		return
	}
	if b := branches(m); len(b) > 0 {
		c.walkBranches(path, b, v)
		return
	}
	if ts := types(m); len(ts) > 0 {
		k := kind(v)
		if !ts[k] && !(k == "integer" && ts["number"]) {
			delete(ts, "null")
			want := make([]string, 0, len(ts))
			for t := range ts {
				want = append(want, t)
			}
			slices.Sort(want)
			c.add(Error, path, fmt.Sprintf("is %s; the contract declares %s", k, strings.Join(want, " or ")))
			return
		}
	}
	switch v := v.(type) {
	case map[string]any:
		c.walkObject(path, m, v)
	case []any:
		c.walkArray(path, m, v)
	}
}

func (c *checker) walkObject(path string, m map[string]any, v map[string]any) {
	props, hasProps := m["properties"].(map[string]any)
	required, _ := m["required"].([]any)
	for _, r := range required {
		if name, _ := r.(string); name != "" {
			if _, ok := v[name]; !ok {
				c.add(Error, join(path, name), "absent, but the contract requires it")
			}
		}
	}
	extra, _ := m["additionalProperties"].(map[string]any)
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		switch p, ok := props[k]; {
		case ok:
			c.walk(join(path, k), p, v[k])
		case extra != nil:
			c.walk(join(path, "{}"), extra, v[k])
		case hasProps && m["additionalProperties"] == nil:
			c.add(Warn, join(path, k), "served, but the contract does not declare it")
		}
	}
}

func (c *checker) walkArray(path string, m map[string]any, v []any) {
	if len(v) == 0 && path != "" {
		c.add(Info, path, "empty array, so its items were not measured")
	}
	if prefix, ok := m["prefixItems"].([]any); ok {
		for i, e := range v {
			if i < len(prefix) {
				c.walk(fmt.Sprintf("%s[%d]", path, i), prefix[i], e)
			}
		}
		return
	}
	for _, e := range v {
		c.walk(join(path, "[]"), m["items"], e)
	}
}

// walkBranches judges v against a oneOf or anyOf: the branch that fits best
// (fewest errors, then fewest warnings) is the one reported.
func (c *checker) walkBranches(path string, b []any, v any) {
	var best *checker
	for _, branch := range b {
		if f := c.flatten(branch, 0); f != nil && len(types(f)) == 1 && types(f)["null"] {
			continue
		}
		try := &checker{schemas: c.schemas}
		try.walk(path, branch, v)
		if best == nil || try.count(Error) < best.count(Error) ||
			try.count(Error) == best.count(Error) && try.count(Warn) < best.count(Warn) {
			best = try
		}
	}
	if best == nil {
		c.add(Error, path, "a value, but the contract declares only null")
		return
	}
	for _, f := range best.out {
		for range f.N {
			c.add(f.Severity, f.Path, f.Msg)
		}
	}
}
