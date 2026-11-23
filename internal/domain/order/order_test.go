package order

import (
	"testing"

	"github.com/WeijianHuang/porch-os/internal/domain/money"
	"github.com/shopspring/decimal"
)

func TestOrderStateMachine(t *testing.T) {
	valid := [][2]string{
		{StatusDraft, StatusOpen}, {StatusDraft, StatusCancelled},
		{StatusOpen, StatusPendingPayment}, {StatusOpen, StatusCancelled},
		{StatusPendingPayment, StatusPaid}, {StatusPendingPayment, StatusCancelled},
		{StatusPaid, StatusCompleted}, {StatusPaid, StatusRefunded},
		{StatusPaid, StatusPartiallyRefunded},
		{StatusCompleted, StatusRefunded}, {StatusCompleted, StatusPartiallyRefunded},
		{StatusPartiallyRefunded, StatusRefunded},
		{StatusPartiallyRefunded, StatusPartiallyRefunded},
	}
	for _, tr := range valid {
		if err := CanOrderTransition(tr[0], tr[1]); err != nil {
			t.Errorf("expected %s -> %s valid: %v", tr[0], tr[1], err)
		}
	}
	invalid := [][2]string{
		{StatusPaid, StatusOpen}, // DP-05 no rollback
		{StatusCompleted, StatusPendingPayment},
		{StatusCancelled, StatusOpen},
		{StatusRefunded, StatusPartiallyRefunded},
		{StatusDraft, StatusPaid},
		{StatusPaid, "refunding"}, // no refunding state exists
	}
	for _, tr := range invalid {
		if err := CanOrderTransition(tr[0], tr[1]); err == nil {
			t.Errorf("expected %s -> %s invalid", tr[0], tr[1])
		}
	}
}

func TestRefundOrderStatus(t *testing.T) {
	cases := []struct {
		from   string
		full   bool
		want   string
		hasErr bool
	}{
		{StatusPaid, true, StatusRefunded, false},
		{StatusPaid, false, StatusPartiallyRefunded, false},
		{StatusCompleted, false, StatusPartiallyRefunded, false},
		{StatusPartiallyRefunded, true, StatusRefunded, false},
		{StatusCancelled, true, "", true},
		{StatusOpen, false, "", true},
	}
	for _, tc := range cases {
		got, err := RefundOrderStatus(tc.from, tc.full)
		if (err != nil) != tc.hasErr || (!tc.hasErr && got != tc.want) {
			t.Errorf("RefundOrderStatus(%s,%v) = %s,%v", tc.from, tc.full, got, err)
		}
	}
}

func TestCanFinish(t *testing.T) {
	if err := CanFinish(StatusPending, StatusSucceeded); err != nil {
		t.Fatal(err)
	}
	if err := CanFinish(StatusPending, StatusFailed); err != nil {
		t.Fatal(err)
	}
	if err := CanFinish(StatusFailed, StatusSucceeded); err == nil {
		t.Fatal("failed is terminal")
	}
	if err := CanFinish(StatusSucceeded, StatusFailed); err == nil {
		t.Fatal("succeeded cannot change")
	}
}

func mustMoney(t *testing.T, s string) money.Money {
	t.Helper()
	m, err := money.FromString(s)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAllocateByWeightsSumsExactly(t *testing.T) {
	cases := []struct {
		total   string
		weights []string
	}{
		{"0.01", []string{"10.00", "10.00", "10.00"}},
		{"1.00", []string{"10.00", "10.00", "10.00"}},
		{"88.88", []string{"100.00", "50.25", "30.10"}},
		{"0.00", []string{"0.00", "0.00"}},
		{"5.00", []string{"0.00", "0.00"}},
	}
	for _, tc := range cases {
		ws := make([]money.Money, len(tc.weights))
		for i, s := range tc.weights {
			ws[i] = mustMoney(t, s)
		}
		parts, err := AllocateByWeights(mustMoney(t, tc.total), ws)
		if err != nil {
			t.Fatalf("total %s: %v", tc.total, err)
		}
		sum := money.Zero()
		for _, p := range parts {
			sum = sum.Add(p)
		}
		if sum.String() != tc.total {
			t.Errorf("total %s: parts sum to %s", tc.total, sum.String())
		}
	}
}

func TestAllocateDeterministic(t *testing.T) {
	ws := []money.Money{mustMoney(t, "33.33"), mustMoney(t, "33.33"), mustMoney(t, "33.34")}
	a, _ := AllocateByWeights(mustMoney(t, "0.02"), ws)
	b, _ := AllocateByWeights(mustMoney(t, "0.02"), ws)
	for i := range a {
		if a[i].String() != b[i].String() {
			t.Fatalf("nondeterministic at %d", i)
		}
	}
	if _, err := AllocateByWeights(mustMoney(t, "1.00"), nil); err == nil {
		t.Fatal("empty weights must fail")
	}
}

func TestLineAmountAndDerive(t *testing.T) {
	amt, err := LineAmount(mustMoney(t, "19.99"), decimal.RequireFromString("3"))
	if err != nil || amt.String() != "59.97" {
		t.Fatalf("line amount = %s, %v", amt.String(), err)
	}
	if _, err := LineAmount(mustMoney(t, "10.00"), decimal.RequireFromString("-1")); err == nil {
		t.Fatal("negative quantity must fail")
	}
	d := DeriveAmounts(mustMoney(t, "100.00"), mustMoney(t, "30.00"))
	if d.Paid.String() != "100.00" || d.Refunded.String() != "30.00" || d.Refundable.String() != "70.00" {
		t.Fatalf("derived = %+v", d)
	}
	d = DeriveAmounts(mustMoney(t, "10.00"), mustMoney(t, "30.00"))
	if d.Refundable.IsNegative() {
		t.Fatal("refundable must floor at zero")
	}
}

func TestQuoteMergedAdjustmentAllocation(t *testing.T) {
	lines := []QuoteLine{
		{LineNo: 1, Amount: mustMoney(t, "60.00")},
		{LineNo: 2, Amount: mustMoney(t, "40.00")},
	}
	// Two adjustments must be MERGED before one allocation (ADR-0019 §2).
	q, err := Quote(lines, []money.Money{mustMoney(t, "5.00"), mustMoney(t, "5.00")})
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	if q.Subtotal.String() != "100.00" || q.DiscountAmount.String() != "10.00" || q.TotalAmount.String() != "90.00" {
		t.Fatalf("totals = %+v", q)
	}
	sum := money.Zero()
	for _, l := range q.Lines {
		sum = sum.Add(l.AllocatedAdjustment)
		if l.LineTotal.String() != l.Amount.Sub(l.AllocatedAdjustment).String() {
			t.Fatalf("line_total mismatch: %+v", l)
		}
	}
	if sum.String() != "10.00" {
		t.Fatalf("allocated sum = %s", sum.String())
	}
	if q.Lines[0].AllocatedAdjustment.String() != "6.00" || q.Lines[1].AllocatedAdjustment.String() != "4.00" {
		t.Fatalf("weight allocation wrong: %+v", q.Lines)
	}
}

func TestQuoteRejectsOverDiscountAndEmpty(t *testing.T) {
	lines := []QuoteLine{{LineNo: 1, Amount: mustMoney(t, "10.00")}}
	if _, err := Quote(lines, []money.Money{mustMoney(t, "10.01")}); err == nil {
		t.Fatal("discount over subtotal must fail")
	}
	if _, err := Quote(nil, nil); err == nil {
		t.Fatal("empty cart must fail")
	}
}
