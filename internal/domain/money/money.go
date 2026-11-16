// Package money is a dependency-free value object for the single CNY currency.
// JSON serializes the amount as a string so clients never cross a float boundary
// (design/03 §1). ADR-0004 forbids a currency field and any conversion.
package money

import (
	"encoding/json"
	"fmt"

	"github.com/shopspring/decimal"
)

// Money is an exact amount with fixed 2-decimal rounding.
type Money struct{ amount decimal.Decimal }

var two = decimal.NewFromInt(100)

func Zero() Money { return Money{decimal.Zero} }

func FromString(s string) (Money, error) {
	d, err := decimal.NewFromString(s)
	if err != nil {
		return Zero(), fmt.Errorf("invalid money %q", s)
	}
	return FromDecimal(d), nil
}

func FromDecimal(d decimal.Decimal) Money {
	return Money{d.Round(2)}
}

func (m Money) Decimal() decimal.Decimal { return m.amount }
func (m Money) String() string           { return m.amount.StringFixed(2) }
func (m Money) IsNegative() bool         { return m.amount.IsNegative() }
func (m Money) Add(o Money) Money        { return FromDecimal(m.amount.Add(o.amount)) }
func (m Money) Sub(o Money) Money        { return FromDecimal(m.amount.Sub(o.amount)) }

// Allocate splits an amount into n shares, distributing the rounding remainder
// deterministically so the parts always sum back to the original amount.
func (m Money) Allocate(n int) ([]Money, error) {
	if n <= 0 {
		return nil, fmt.Errorf("allocate: n must be positive")
	}
	cents := m.amount.Mul(two).Truncate(0).IntPart()
	base, remainder := cents/int64(n), cents%int64(n)
	out := make([]Money, n)
	for i := range out {
		c := base
		if int64(i) < remainder {
			c++
		}
		out[i] = Money{decimal.NewFromInt(c).Div(two)}
	}
	return out, nil
}

func (m Money) MarshalJSON() ([]byte, error) { return json.Marshal(m.String()) }

func (m *Money) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := FromString(s)
	if err != nil {
		return err
	}
	*m = v
	return nil
}
