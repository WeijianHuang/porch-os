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

// QuoteLine is one priced line in a quote (ADR-0019): the caller supplies the
// line number and already-snapshotted amount; the pure function never reads a
// catalog or a database.
type QuoteLine struct {
	LineNo int
	Amount money.Money
}

// QuoteLineResult is one priced line with its share of the merged discount.
// The first two fields are filled by the store from the catalog snapshot;
// Quote itself only computes amounts and leaves them empty for submit reuse.
type QuoteLineResult struct {
	LineNo              int         `json:"line_no"`
	CatalogItemID       string      `json:"catalog_item_id,omitempty"`
	ItemNameSnapshot    string      `json:"item_name_snapshot,omitempty"`
	Quantity            string      `json:"quantity,omitempty"`
	Amount              money.Money `json:"amount"`
	AllocatedAdjustment money.Money `json:"allocated_adjustment"`
	LineTotal           money.Money `json:"line_total"`
}

// QuoteResult is the whole-order quote shared by POST /orders/price and the
// submit re-price path (ADR-0019 §4): one function so preview and checkout can
// never drift.
type QuoteResult struct {
	Lines          []QuoteLineResult `json:"lines"`
	Subtotal       money.Money       `json:"subtotal"`
	DiscountAmount money.Money       `json:"discount_amount"`
	TotalAmount    money.Money       `json:"total_amount"`
}

// Quote merges all adjustments into one discount, allocates it once across
// the lines by amount weight via AllocateByWeights (ADR-0019 §2, ADR-0020),
// and returns per-line totals plus order-level subtotal/discount/total. A
// discount greater than the subtotal is rejected (422 at the HTTP boundary).
func Quote(lines []QuoteLine, adjustments []money.Money) (QuoteResult, error) {
	var out QuoteResult
	if len(lines) == 0 {
		return out, errors.New("quote: no lines")
	}
	weights := make([]money.Money, len(lines))
	out.Subtotal = money.Zero()
	for i, l := range lines {
		if l.Amount.IsNegative() {
			return out, errors.New("quote: negative line amount")
		}
		weights[i] = l.Amount
		out.Subtotal = out.Subtotal.Add(l.Amount)
	}
	for _, a := range adjustments {
		if a.IsNegative() {
			return out, errors.New("quote: negative adjustment")
		}
		out.DiscountAmount = out.DiscountAmount.Add(a)
	}
	if out.DiscountAmount.Decimal().GreaterThan(out.Subtotal.Decimal()) {
		return out, errors.New("quote: discount exceeds subtotal")
	}
	allocated, err := AllocateByWeights(out.DiscountAmount, weights)
	if err != nil {
		return out, err
	}
	out.TotalAmount = out.Subtotal.Sub(out.DiscountAmount)
	out.Lines = make([]QuoteLineResult, len(lines))
	for i, l := range lines {
		out.Lines[i] = QuoteLineResult{
			LineNo:              l.LineNo,
			Amount:              l.Amount,
			AllocatedAdjustment: allocated[i],
			LineTotal:           l.Amount.Sub(allocated[i]),
		}
	}
	return out, nil
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
