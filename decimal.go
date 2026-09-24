package nexus

import "github.com/nexus-xyz/nexus-exchange-go/internal/models"

// Decimal is an exact decimal number as the Exchange API serves it: prices,
// quantities, balances, fees and PnL. It holds the served string unchanged,
// so "1.50" decodes and re-encodes as "1.50", never "1.5". It is never a
// float64; see the type's own documentation for why, and for the accepted
// literal forms.
//
// The type is declared next to the generated models, and aliased here, because
// the models use it for every money field and this package imports the models:
// declaring it here would make that an import cycle. It is the same type, with
// the same methods, either way.
type Decimal = models.Decimal

// ParseDecimal validates s as a decimal literal (see [Decimal] for the accepted
// forms) and returns it unchanged as a Decimal.
func ParseDecimal(s string) (Decimal, error) { return models.ParseDecimal(s) }
