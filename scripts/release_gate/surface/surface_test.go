package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fixture is a module with one public package, one internal package it aliases into, an
// example-like main package and a test file, which between them hold every case the listing
// has a rule for.
var fixture = map[string]string{
	"go.mod": "module example.com/sdk\n\ngo 1.22\n",
	"sdk.go": `package sdk

import (
	"errors"
	"fmt"

	"example.com/sdk/internal/models"
)

type Client struct {
	Name   string
	hidden int
	*Embedded
	models.Base
	internalOnly
	A, B  int
	OnErr func(code int, msg string) error
}

type Embedded struct{}

type internalOnly struct{}

func (c *Client) Do(ctx int, args ...string) (n int, err error) { return 0, nil }

func (c Client) Value() string { return "" }

func (c *Client) private() {}

func (i internalOnly) Promoted() {}

func New(name string) *Client { return nil }

func helper() {}

type List[T any] struct{ Items []T }

func (l *List[T]) Push(v T) {}

func Map[T, U any](xs []T, f func(T) U) []U { return nil }

type Number interface{ ~int | ~float64 }

type Reader interface {
	Read(p []byte) (n int, err error)
	seal()
	fmt.Stringer
}

type Option func(*Client)

type Kind int

const (
	KindA Kind = iota + 1
	KindB
	kindHidden
)

const Limit = 5

const Buy = models.SideBuy

type Side = models.Side

type Model = models.Model

var ErrGone = errors.New("gone, whatever the wording")

var ErrWrapped = fmt.Errorf("wrapped: %w", ErrGone)

var ErrInternal = models.ErrInternal

var Default = Kind(1)

var Typed Kind

var Tag = Channel{name: "orders"}

type Channel struct{ name string }

var hidden = 1
`,
	"sdk_test.go": "package sdk\n\nfunc TestOnly() {}\n",
	"internal/models/models.go": `package models

import "errors"

type Model struct {
	ID     *string
	Status *Status
	secret int
}

func (m *Model) Get() string { return "" }

func (m Model) hidden() {}

type Status string

type Side string

const SideBuy Side = "Buy"

type Base struct{}

var ErrInternal = errors.New("internal")
`,
	"examples/run/main.go": "package main\n\nfunc Exported() {}\n\nfunc main() {}\n",
}

func listFixture(t *testing.T) []string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range fixture {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	lines, err := Surface(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.IsSorted(lines) {
		t.Error("the listing is not sorted")
	}
	return lines
}

func TestSurface(t *testing.T) {
	lines := listFixture(t)
	t.Logf("listing:\n%s", strings.Join(lines, "\n"))

	present := []string{
		// Funcs: parameter names left out, variadics kept, generics with their type parameters.
		"func sdk.New(string) *Client",
		"func sdk.Map[T, U any]([]T, func(T) U) []U",
		// Methods: pointer and value receivers shown, a generic receiver with its parameters.
		"func (*sdk.Client) Do(int, ...string) (int, error)",
		"func (sdk.Client) Value() string",
		"func (*sdk.List[T]) Push(T)",
		// Types: kind and fields, embedded ones (local and from another package) included.
		"type sdk.Client struct",
		"type sdk.Client struct { Name string }",
		"type sdk.Client struct { A int }",
		"type sdk.Client struct { B int }",
		"type sdk.Client struct { *Embedded }",
		"type sdk.Client struct { models.Base }",
		"type sdk.Client struct { OnErr func(int, string) error }",
		"type sdk.List[T any] struct",
		"type sdk.List struct { Items []T }",
		"type sdk.Number interface",
		"type sdk.Number interface { ~int | ~float64 }",
		"type sdk.Reader interface",
		"type sdk.Reader interface { Read([]byte) (int, error) }",
		"type sdk.Reader interface { fmt.Stringer }",
		"type sdk.Option func(*Client)",
		"type sdk.Kind int",
		// Consts with type and value; an implicit repetition with its iota.
		"const sdk.KindA Kind = iota + 1 (iota = 0)",
		"const sdk.KindB Kind = iota + 1 (iota = 1)",
		"const sdk.Limit = 5",
		// Followed into the internal package: the enum's wire value, the alias's fields and methods.
		`const sdk.Buy models.Side = "Buy"`,
		"type sdk.Side = models.Side",
		"type sdk.Side string",
		"type sdk.Model = models.Model",
		"type sdk.Model struct",
		"type sdk.Model struct { ID *string }",
		"type sdk.Model struct { Status *models.Status }",
		"func (*sdk.Model) Get() string",
		// Vars: an error is its type, not its message; other values as declared.
		"var sdk.ErrGone error",
		"var sdk.ErrWrapped error",
		"var sdk.ErrInternal error",
		"var sdk.Default = Kind(1)",
		"var sdk.Typed Kind",
		`var sdk.Tag Channel = Channel{name: "orders"}`,
	}
	for _, want := range present {
		if !slices.Contains(lines, want) {
			t.Errorf("missing line %q", want)
		}
	}

	absent := []string{
		"type sdk.Client struct { hidden int }",
		"type sdk.Client struct { internalOnly }",
		"func (*sdk.Client) private()",
		"func (sdk.internalOnly) Promoted()",
		"func sdk.helper()",
		"func sdk.TestOnly()",
		"type sdk.Reader interface { seal() }",
		"type sdk.Model struct { secret int }",
		"func (sdk.Model) hidden()",
		"const sdk.kindHidden Kind = iota + 1 (iota = 2)",
		"var sdk.hidden = 1",
	}
	for _, unwanted := range absent {
		if slices.Contains(lines, unwanted) {
			t.Errorf("unexpected line %q", unwanted)
		}
	}
	// Package main (an example) and internal packages are listed only through an alias.
	for _, line := range lines {
		if strings.Contains(line, "Exported") || strings.Contains(line, "internal/") {
			t.Errorf("line from an unlisted package: %q", line)
		}
	}
}
