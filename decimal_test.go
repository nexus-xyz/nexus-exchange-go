package nexus

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"math/big"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecimalRoundTrip(t *testing.T) {
	for _, in := range []string{
		`"1.50"`, `"0.00000100"`, `"-42.1000"`, `"0"`, `"-0.5"`,
		`"79228162514264337593543950335"`, `"123456789012345678901234567890.123456789012345678"`,
	} {
		var d Decimal
		if err := json.Unmarshal([]byte(in), &d); err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		out, err := json.Marshal(d)
		if err != nil || string(out) != in {
			t.Errorf("%s round-tripped to %s (err %v)", in, out, err)
		}
	}
}

func TestDecimalRejects(t *testing.T) {
	for _, in := range []string{
		`1.5`, `150`, `true`, `""`, `"1e3"`, `"1.5E-2"`, `"+1"`, `"NaN"`, `"Infinity"`,
		`"01.5"`, `"1."`, `".5"`, `" 1"`, `"1,5"`, `"\u0031"`,
	} {
		var d Decimal
		if err := json.Unmarshal([]byte(in), &d); err == nil {
			t.Errorf("%s: accepted as %q", in, d.String())
		}
	}
}

func TestDecimalNullAndZero(t *testing.T) {
	d, _ := ParseDecimal("2.0")
	if err := json.Unmarshal([]byte(`null`), &d); err != nil || d.String() != "2.0" {
		t.Errorf("null: got %q, %v", d.String(), err)
	}
	var p *Decimal
	if err := json.Unmarshal([]byte(`null`), &p); err != nil || p != nil {
		t.Errorf("null into *Decimal: got %v, %v", p, err)
	}
	if out, _ := json.Marshal(Decimal{}); string(out) != `"0"` {
		t.Errorf("zero value encodes as %s", out)
	}
}

func TestDecimalConversions(t *testing.T) {
	d, _ := ParseDecimal("0.10")
	if d.Rat().Cmp(big.NewRat(1, 10)) != 0 {
		t.Errorf("Rat() = %v", d.Rat())
	}
	if f, err := d.Float64(); err != nil || f != 0.1 {
		t.Errorf("Float64() = %v, %v", f, err)
	}
}

// ccxtTrade stands in for the CCXT-shaped models of module 6: the numeric
// timestamp and the ISO-8601 datetime are both served and both kept.
type ccxtTrade struct {
	Timestamp int64   `json:"timestamp"`
	Datetime  string  `json:"datetime"`
	Price     Decimal `json:"price"`
}

func TestCCXTTimestampDatetimeRoundTrip(t *testing.T) {
	in := `{"timestamp":1758700800123,"datetime":"2025-09-24T08:00:00.123Z","price":"65000.10"}`
	var tr ccxtTrade
	if err := json.Unmarshal([]byte(in), &tr); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(tr)
	if string(out) != in {
		t.Errorf("round trip:\n got %s\nwant %s", out, in)
	}
}

// TestNoFloatsInWireTypes parses the source of every package that declares wire
// types and fails on float32 or float64 anywhere in a type declaration (struct
// fields, slices, maps, named types). Parsing the AST, not grepping, means no
// formatting or aliasing trick hides one. Method signatures are allowed, which
// is what lets Decimal.Float64 exist as an explicit escape hatch.
func TestNoFloatsInWireTypes(t *testing.T) {
	for _, dir := range []string{".", "internal/models"} {
		files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		fset := token.NewFileSet()
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(f, func(n ast.Node) bool {
				spec, ok := n.(*ast.TypeSpec)
				if !ok {
					return true
				}
				ast.Inspect(spec.Type, func(n ast.Node) bool {
					if id, ok := n.(*ast.Ident); ok && (id.Name == "float64" || id.Name == "float32") {
						t.Errorf("%s: %s in type %s; money is a Decimal, time an int64", fset.Position(id.Pos()), id.Name, spec.Name.Name)
					}
					return true
				})
				return false
			})
		}
	}
}
