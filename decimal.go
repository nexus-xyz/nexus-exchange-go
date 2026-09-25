package nexus

import "github.com/nexus-xyz/nexus-exchange-go/internal/models"

// Decimal is an exact decimal number as the Exchange API serves it: prices,
// quantities, balances, fees and PnL. It holds the served string unchanged,
// so "1.50" decodes and re-encodes as "1.50", never "1.5".
//
// Do not convert it to float64 on the way in or out. The API serves every
// monetary value as a decimal string (R2.9) precisely so that no client does
// float arithmetic on money, and it serves Order.remaining itself rather than
// leave clients to compute amount - filled, because that drifts in floating
// point (ENG-13272). Most decimal fractions, 0.1 among them, have no exact
// float64 form. Build values from strings with [ParseDecimal], do exact
// arithmetic with [Decimal.Rat] (math/big), or hand [Decimal.String] to the
// decimal library of your choice. [Decimal.Float64] is lossy and exists for
// display only.
//
// A literal is an optional "-", an integer part without superfluous leading
// zeros, and an optional fraction: "0", "-12", "1.50". Exponents, a leading
// "+", NaN and JSON numbers are rejected. The zero value is the number zero.
//
// The type is declared next to the generated models, and aliased here, because
// the models use it for every money field and this package imports the models:
// declaring it here would make that an import cycle. It is the same type, with
// the same methods, either way.
type Decimal = models.Decimal

// ParseDecimal validates s as a decimal literal (see [Decimal] for the accepted
// forms) and returns it unchanged as a Decimal.
func ParseDecimal(s string) (Decimal, error) { return models.ParseDecimal(s) }
