package order

import (
	"errors"

	"github.com/WeijianHuang/porch-os/internal/domain/money"
	"github.com/shopspring/decimal"
)

// AllocateByWeights splits a whole-order adjustment across lines in proportion
// to the supplied weights (normally each line's amount), guaranteeing the
// parts sum back to exactly the total. The rounding remainder is assigned in
// weight order so the same input always yields the same result (design/01 §6).
// A zero-weight line receives nothing; if all weights are zero the remainder
// falls on the first line so the invariant still holds.
func AllocateByWeights(total money.Money, weights []money.Money) ([]money.Money, error) {
	if len(weights) == 0 {
		return nil, errors.New("allocate: no weights")
	}
	unit := decimal.RequireFromString("0.01")
	totalCents := total.Decimal().Div(unit).Truncate(0).IntPart()
	if totalCents < 0 {
		return nil, errors.New("allocate: total must not be negative")
	}

	ws := make([]decimal.Decimal, len(weights))
	var sum decimal.Decimal
	for i, w := range weights {
		if w.Decimal().IsNegative() {
			return nil, errors.New("allocate: negative weight")
		}
		ws[i] = w.Decimal()
		sum = sum.Add(ws[i])
	}

	out := make([]money.Money, len(weights))
	if sum.IsZero() {
		// No basis for proportional split; put the whole amount on line 0.
		out[0] = money.FromDecimal(decimal.NewFromInt(totalCents).Mul(unit))
		return out, nil
	}

	allocated := int64(0)
	shares := make([]int64, len(weights))
	for i, w := range ws {
		shares[i] = w.Mul(decimal.NewFromInt(totalCents)).Div(sum).Truncate(0).IntPart()
		allocated += shares[i]
	}
	// Distribute the sub-cent remainder one cent at a time, in line order.
	for i := 0; allocated < totalCents; i++ {
		shares[i]++
		allocated++
	}
	for i := range shares {
		out[i] = money.FromDecimal(decimal.NewFromInt(shares[i]).Mul(unit))
	}
	return out, nil
}

// LineAmount computes a line's snapshot amount as unit_price × quantity,
// rounded once to cents with the fixed scale (single CNY, no float path).
func LineAmount(unitPrice money.Money, quantity decimal.Decimal) (money.Money, error) {
	if quantity.IsNegative() {
		return money.Zero(), errors.New("quantity must not be negative")
	}
	return money.FromDecimal(unitPrice.Decimal().Mul(quantity)), nil
}

// DerivedAmounts are computed from immutable records, never persisted
// (ADR-0014). paid - refunded is floored at zero: the refund checks in the
// write path guarantee refunded never exceeds paid.
type DerivedAmounts struct {
	Paid       money.Money `json:"paid_amount"`
	Refunded   money.Money `json:"refunded_amount"`
	Refundable money.Money `json:"refundable_amount"`
}

func DeriveAmounts(paid, refunded money.Money) DerivedAmounts {
	refundable := paid.Sub(refunded)
	if refundable.IsNegative() {
		refundable = money.Zero()
	}
	return DerivedAmounts{Paid: paid, Refunded: refunded, Refundable: refundable}
}
