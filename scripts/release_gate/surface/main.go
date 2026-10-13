// Command surface lists the public API of the Go module in the directory it is given, one sorted
// line per exported item (ENG-18798). scripts/release_gate/public_api.sh runs it and compares the
// output with the committed public-api.txt; the verdict compares that file with its copy at the
// published tag.
//
//	go run . <module root>
//
// It reads the non-test .go files of every package `go list ./...` finds there, except package
// main (the examples) and anything under internal/ (testdata is never listed), with go/parser and
// go/ast. No type checker, so it needs no dependency downloaded. Each line is one of:
//
//	func nexus.NewClient(Network, ...Option) (*Client, error)    a func: type parameters, then parameter and result types
//	func (*nexus.Client) FetchMarkets(context.Context) (...)    a method with an exported name on an exported type, receiver shown
//	type nexus.Network int                                      a type: its kind or underlying type, type parameters
//	type nexus.Page struct { Limit int }                        an exported struct field, embedded ones included, with its type
//	type nexus.Event interface { ... }                          an interface method or embedded element
//	const nexus.Buy models.OrderRequestSide = "Buy"             a const with its type and value
//	var nexus.ErrNotFound error                                 a var with its type
//
// Parameter and result names are left out: they are not part of the API, so renaming one moves
// nothing. A const's value is in, so an enum's wire value changing moves a line; an implicit
// repetition in a const block shows the expression it repeats and its iota. A var built with
// errors.New or fmt.Errorf is listed as `error`, so rewording its message moves nothing; one
// built from a composite literal keeps the literal (a Channel's wire name is in it).
//
// Aliases into the module's own packages are followed. Most of this SDK's types are aliases of
// generated types in internal/models (`MarketSummary = models.MarketSummary`), and its enum
// values are consts of internal ones (`Buy = models.OrderRequestSideBuy`), so listing the alias
// alone would miss every field and every wire value. An alias of a type in this module also lists
// the target's kind, fields and methods under the alias's name, with the target package's own
// names qualified (`*models.MarketSummaryStatus`); a const or var whose value names one in this
// module is listed with that one's type and value.
//
// What it does not see: what an embedded type promotes; struct tags, so a JSON wire name (the
// spec-drift and conformance checks own those); unexported fields, so adding one to a struct whose
// fields were all exported, which breaks unkeyed literals; and types reachable only through a
// field or a result. Doc comments are not part of the listing, so rewording one moves nothing.
package main

import (
	"bufio"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run . <module root>")
		os.Exit(64)
	}
	lines, err := Surface(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "surface:", err)
		os.Exit(1)
	}
	w := bufio.NewWriter(os.Stdout)
	for _, line := range lines {
		fmt.Fprintln(w, line)
	}
	if err := w.Flush(); err != nil {
		fmt.Fprintln(os.Stderr, "surface:", err)
		os.Exit(1)
	}
}
