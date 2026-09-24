package nexus

import (
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
)

// Decimal is an exact decimal number as the Exchange API serves it: prices,
// quantities, balances, fees and PnL. It holds the served string unchanged,
// so "1.50" decodes and re-encodes as "1.50", never "1.5".
//
// # Why a string, and why not float64
//
// The API serializes every monetary value as a JSON string produced by
// rust_decimal's to_string (spec rule R2.9), so that no client does float
// arithmetic on money. The contract pays for that rule on purpose: Order.remaining
// is served by the exchange rather than derived as amount - filled, because
// deriving it in a float-based client drifts (ENG-13272). Decoding these values
// into float64 would silently throw that away, and encoding/json would do it
// without complaint. No wire struct in this SDK has a float64 field, and a test
// keeps it that way.
//
// # Why an SDK-owned type, and why not a decimal library
//
// Decimal carries no arithmetic. A third-party decimal type in the public API
// would be a permanent dependency in every caller's type signatures, and would
// choose a precision model on the caller's behalf. Instead, callers convert
// explicitly: [Decimal.Rat] for exact arithmetic with math/big,
// [Decimal.String] to feed the decimal library of their choice, and
// [Decimal.Float64] only when they accept the precision loss.
//
// # Accepted forms
//
// A valid literal is an optional "-", an integer part without superfluous
// leading zeros, and an optional fraction: "0", "-12", "1.50", "0.00000100".
// JSON numbers, exponent forms ("1e3"), a leading "+", "NaN", "Infinity" and
// the empty string are rejected, because the exchange never serves them and
// accepting them would hide a contract break. JSON null leaves the value
// unchanged, following the encoding/json convention; use *Decimal for fields
// that can be null.
//
// The zero value is the number zero and encodes as "0".
type Decimal struct{ s string }

var decimalLiteral = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

// ParseDecimal validates s as a decimal literal (see [Decimal] for the accepted
// forms) and returns it unchanged as a Decimal.
func ParseDecimal(s string) (Decimal, error) {
	if !decimalLiteral.MatchString(s) {
		return Decimal{}, fmt.Errorf("nexus: invalid decimal %q", s)
	}
	return Decimal{s}, nil
}

// String returns the decimal exactly as served, or "0" for the zero value.
func (d Decimal) String() string {
	if d.s == "" {
		return "0"
	}
	return d.s
}

// Rat returns the exact value as a new big.Rat.
func (d Decimal) Rat() *big.Rat {
	r, _ := new(big.Rat).SetString(d.String()) // valid by construction
	return r
}

// Float64 converts to the nearest float64. This is LOSSY: most decimal
// fractions, 0.1 among them, have no exact float64 form, so never do money
// arithmetic on the result or send it back to the exchange. It fails only
// when the value is out of float64 range.
func (d Decimal) Float64() (float64, error) {
	return strconv.ParseFloat(d.String(), 64)
}

// MarshalJSON encodes d as a JSON string holding the served literal.
func (d Decimal) MarshalJSON() ([]byte, error) {
	return []byte(`"` + d.String() + `"`), nil
}

// UnmarshalJSON accepts only a JSON string holding a decimal literal, or null
// (which leaves d unchanged). A JSON number is an error, not a conversion.
func (d *Decimal) UnmarshalJSON(b []byte) error {
	s := string(b)
	if s == "null" {
		return nil
	}
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return errors.New("nexus: decimal must be a JSON string, got " + s)
	}
	// A valid literal has no characters that need JSON escaping, so the raw
	// bytes between the quotes are the literal; anything escaped fails the match.
	v, err := ParseDecimal(s[1 : len(s)-1])
	if err != nil {
		return err
	}
	*d = v
	return nil
}
