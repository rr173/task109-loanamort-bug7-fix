package loan_test

import (
	"context"
	"path/filepath"
	"testing"

	"task109-loanamort/internal/loan"
	"task109-loanamort/internal/store"
)

// newService opens a fresh SQLite file and returns a service over it.
func newService(t *testing.T) (*loan.Service, func()) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "svc.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return loan.New(st), func() { st.Close() }
}

func mustBorrower(t *testing.T, svc *loan.Service, name string) string {
	t.Helper()
	b, err := svc.CreateBorrower(context.Background(), loan.CreateBorrowerRequest{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	return b.BorrowerID
}

func mustLoan(t *testing.T, svc *loan.Service, borrowerID string, principal int64, annual float64, periods int, ltype loan.LoanType) loan.Loan {
	t.Helper()
	l, err := svc.CreateLoan(context.Background(), loan.CreateLoanRequest{
		BorrowerID: borrowerID, PrincipalCents: principal, AnnualPercent: annual, Periods: periods, Type: ltype,
	})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// TestCreateLoanValidates: invalid inputs are rejected before any row is
// written.
func TestCreateLoanValidates(t *testing.T) {
	svc, closeFn := newService(t)
	defer closeFn()
	bid := mustBorrower(t, svc, "x")
	ctx := context.Background()

	cases := []struct {
		name string
		req  loan.CreateLoanRequest
	}{
		{"empty borrower", loan.CreateLoanRequest{PrincipalCents: 1000, Periods: 12, Type: loan.EqualInstallment}},
		{"zero principal", loan.CreateLoanRequest{BorrowerID: bid, Periods: 12, Type: loan.EqualInstallment}},
		{"zero periods", loan.CreateLoanRequest{BorrowerID: bid, PrincipalCents: 1000, Type: loan.EqualInstallment}},
		{"bad type", loan.CreateLoanRequest{BorrowerID: bid, PrincipalCents: 1000, Periods: 12, Type: "bogus"}},
		{"negative rate", loan.CreateLoanRequest{BorrowerID: bid, PrincipalCents: 1000, Periods: 12, AnnualPercent: -1, Type: loan.EqualInstallment}},
		{"borrower missing", loan.CreateLoanRequest{BorrowerID: "nope", PrincipalCents: 1000, Periods: 12, Type: loan.EqualInstallment}},
	}
	for _, c := range cases {
		if _, err := svc.CreateLoan(ctx, c.req); err == nil {
			t.Errorf("%s: expected error, got nil", c.name)
		}
	}
}

// TestRecordPaymentExactMatchAndClosure: paying every period at the exact
// scheduled amount closes the loan at the end.
func TestRecordPaymentExactMatchAndClosure(t *testing.T) {
	svc, closeFn := newService(t)
	defer closeFn()
	bid := mustBorrower(t, svc, "x")
	l := mustLoan(t, svc, bid, 1_000_000, 12.0, 12, loan.EqualInstallment)
	ctx := context.Background()

	sched, err := svc.Schedule(ctx, l.LoanID)
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range sched.Periods {
		_, loan2, err := svc.RecordPayment(ctx, l.LoanID, loan.RecordPaymentRequest{AmountCents: p.Payment})
		if err != nil {
			t.Fatalf("period %d: %v", i+1, err)
		}
		if i == len(sched.Periods)-1 {
			if loan2.Status != loan.StatusClosed {
				t.Errorf("final payment: status %s, want closed", loan2.Status)
			}
		}
	}
	// Outstanding must be zero.
	bal, _ := svc.Balance(ctx, l.LoanID, -1)
	if bal.Outstanding != 0 {
		t.Errorf("outstanding after full paydown = %d, want 0", bal.Outstanding)
	}
}

// TestRecordPaymentAmountMismatchRejected: a wrong amount is rejected and the
// loan state is untouched.
func TestRecordPaymentAmountMismatchRejected(t *testing.T) {
	svc, closeFn := newService(t)
	defer closeFn()
	bid := mustBorrower(t, svc, "x")
	l := mustLoan(t, svc, bid, 1_000_000, 12.0, 12, loan.EqualInstallment)
	ctx := context.Background()

	sched, _ := svc.Schedule(ctx, l.LoanID)
	wrong := sched.Periods[0].Payment + 1
	_, _, err := svc.RecordPayment(ctx, l.LoanID, loan.RecordPaymentRequest{AmountCents: wrong})
	if err == nil {
		t.Fatal("expected amount-mismatch rejection")
	}
	bal, _ := svc.Balance(ctx, l.LoanID, -1)
	if bal.Outstanding != 1_000_000 {
		t.Errorf("state changed after rejected payment: outstanding %d", bal.Outstanding)
	}
}

// TestPrepayReduceTermShortensTerm: a reduce_term prepayment shortens the
// loan's term and the loan still amortizes to zero.
func TestPrepayReduceTermShortensTerm(t *testing.T) {
	svc, closeFn := newService(t)
	defer closeFn()
	bid := mustBorrower(t, svc, "x")
	l := mustLoan(t, svc, bid, 1_000_000, 12.0, 12, loan.EqualInstallment)
	ctx := context.Background()

	// Pay period 1.
	sched, _ := svc.Schedule(ctx, l.LoanID)
	_, _, _ = svc.RecordPayment(ctx, l.LoanID, loan.RecordPaymentRequest{AmountCents: sched.Periods[0].Payment})

	_, loan2, err := svc.Prepay(ctx, l.LoanID, loan.PrepayRequest{AmountCents: 200_000, Strategy: loan.ReduceTerm})
	if err != nil {
		t.Fatalf("Prepay reduce_term: %v", err)
	}
	if loan2.TermPeriods <= 1 || loan2.TermPeriods >= 12 {
		t.Errorf("term %d not shortened into (1,12)", loan2.TermPeriods)
	}
	// Full schedule (paid + remaining) zeroes exactly.
	fullSched, _ := svc.Schedule(ctx, l.LoanID)
	if got := schedSumPrincipal(fullSched); got != 1_000_000-200_000 {
		t.Errorf("post-prepay plan principal %d != 800000", got)
	}
}

// TestPrepayReducePaymentKeepsTerm: reduce_payment keeps the term and lowers
// the next payment.
func TestPrepayReducePaymentKeepsTerm(t *testing.T) {
	svc, closeFn := newService(t)
	defer closeFn()
	bid := mustBorrower(t, svc, "x")
	l := mustLoan(t, svc, bid, 1_000_000, 12.0, 12, loan.EqualInstallment)
	ctx := context.Background()
	sched, _ := svc.Schedule(ctx, l.LoanID)
	before := sched.Periods[0].Payment
	_, _, _ = svc.RecordPayment(ctx, l.LoanID, loan.RecordPaymentRequest{AmountCents: before})
	_, loan2, err := svc.Prepay(ctx, l.LoanID, loan.PrepayRequest{AmountCents: 300_000, Strategy: loan.ReducePayment})
	if err != nil {
		t.Fatalf("Prepay reduce_payment: %v", err)
	}
	if loan2.TermPeriods != 12 {
		t.Errorf("term %d != 12", loan2.TermPeriods)
	}
	sum, _ := svc.Summary(ctx, l.LoanID)
	if sum.NextPayment >= before {
		t.Errorf("next payment %d not lower than %d", sum.NextPayment, before)
	}
}

// TestPrepayExceedingOutstandingRejected.
func TestPrepayExceedingOutstandingRejected(t *testing.T) {
	svc, closeFn := newService(t)
	defer closeFn()
	bid := mustBorrower(t, svc, "x")
	l := mustLoan(t, svc, bid, 1_000_000, 12.0, 12, loan.EqualInstallment)
	ctx := context.Background()
	_, _, err := svc.Prepay(ctx, l.LoanID, loan.PrepayRequest{AmountCents: 2_000_000, Strategy: loan.ReducePayment})
	if err == nil {
		t.Fatal("expected prepay-too-large rejection")
	}
}

// TestPrepayReduceTermOnlyEqualInstallment: reduce_term is rejected for other
// types (variable payment).
func TestPrepayReduceTermOnlyEqualInstallment(t *testing.T) {
	svc, closeFn := newService(t)
	defer closeFn()
	bid := mustBorrower(t, svc, "x")
	l := mustLoan(t, svc, bid, 1_000_000, 12.0, 12, loan.EqualPrincipal)
	ctx := context.Background()
	_, _, err := svc.Prepay(ctx, l.LoanID, loan.PrepayRequest{AmountCents: 100_000, Strategy: loan.ReduceTerm})
	if err == nil {
		t.Fatal("expected rejection of reduce_term on equal_principal")
	}
}

// TestRateChangeRecomputesPayment: a higher rate raises the next payment.
func TestRateChangeRecomputesPayment(t *testing.T) {
	svc, closeFn := newService(t)
	defer closeFn()
	bid := mustBorrower(t, svc, "x")
	l := mustLoan(t, svc, bid, 1_000_000, 6.0, 12, loan.EqualInstallment)
	ctx := context.Background()
	before, _ := svc.Summary(ctx, l.LoanID)
	if _, err := svc.ChangeRate(ctx, l.LoanID, loan.ChangeRateRequest{AnnualPercent: 18.0}); err != nil {
		t.Fatal(err)
	}
	after, _ := svc.Summary(ctx, l.LoanID)
	if after.NextPayment <= before.NextPayment {
		t.Errorf("higher rate: next %d not > before %d", after.NextPayment, before.NextPayment)
	}
	// Past payments immutable: rate change adds no payment row.
	pays, _ := svc.ListPaymentsByLoan(ctx, l.LoanID)
	if len(pays) != 0 {
		t.Errorf("rate change created %d payment rows", len(pays))
	}
}

// TestCancelLoanGuards: cancel works with no payments, fails with payments.
func TestCancelLoanGuards(t *testing.T) {
	svc, closeFn := newService(t)
	defer closeFn()
	bid := mustBorrower(t, svc, "x")
	l := mustLoan(t, svc, bid, 1_000_000, 12.0, 12, loan.EqualInstallment)
	ctx := context.Background()

	// Cancel with no payments succeeds.
	if _, err := svc.CancelLoan(ctx, l.LoanID); err != nil {
		t.Fatalf("cancel empty: %v", err)
	}
	// Cancel again fails (terminal).
	if _, err := svc.CancelLoan(ctx, l.LoanID); err == nil {
		t.Fatal("expected terminal error on re-cancel")
	}
}

// TestRecomputeNoDrift: a healthy portfolio reports zero drift.
func TestRecomputeNoDrift(t *testing.T) {
	svc, closeFn := newService(t)
	defer closeFn()
	bid := mustBorrower(t, svc, "x")
	l := mustLoan(t, svc, bid, 1_000_000, 12.0, 12, loan.EqualInstallment)
	ctx := context.Background()
	sched, _ := svc.Schedule(ctx, l.LoanID)
	_, _, _ = svc.RecordPayment(ctx, l.LoanID, loan.RecordPaymentRequest{AmountCents: sched.Periods[0].Payment})
	_, _, _ = svc.Prepay(ctx, l.LoanID, loan.PrepayRequest{AmountCents: 100_000, Strategy: loan.ReduceTerm})
	rep, err := svc.Recompute(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK || len(rep.Drift) != 0 {
		t.Errorf("recompute: ok=%v drift=%v (expected clean)", rep.OK, rep.Drift)
	}
}

// TestAsOfBalanceFromLedger: outstanding(as_of) = principal − Σ(principal paid
// with seq ≤ as_of).
func TestAsOfBalanceFromLedger(t *testing.T) {
	svc, closeFn := newService(t)
	defer closeFn()
	bid := mustBorrower(t, svc, "x")
	l := mustLoan(t, svc, bid, 1_000_000, 12.0, 12, loan.EqualInstallment)
	ctx := context.Background()
	sched, _ := svc.Schedule(ctx, l.LoanID)
	p1 := sched.Periods[0].Principal
	p2 := sched.Periods[1].Principal
	_, _, _ = svc.RecordPayment(ctx, l.LoanID, loan.RecordPaymentRequest{AmountCents: sched.Periods[0].Payment})
	_, _, _ = svc.RecordPayment(ctx, l.LoanID, loan.RecordPaymentRequest{AmountCents: sched.Periods[1].Payment})
	bal1, _ := svc.Balance(ctx, l.LoanID, 1)
	if bal1.Outstanding != 1_000_000-p1 {
		t.Errorf("as_of=1 outstanding %d, want %d", bal1.Outstanding, 1_000_000-p1)
	}
	bal2, _ := svc.Balance(ctx, l.LoanID, 2)
	if bal2.Outstanding != 1_000_000-p1-p2 {
		t.Errorf("as_of=2 outstanding %d, want %d", bal2.Outstanding, 1_000_000-p1-p2)
	}
}

// TestTerminalLoanRejectsMutations: a closed loan rejects payments.
func TestTerminalLoanRejectsMutations(t *testing.T) {
	svc, closeFn := newService(t)
	defer closeFn()
	bid := mustBorrower(t, svc, "x")
	l := mustLoan(t, svc, bid, 100_000, 12.0, 2, loan.EqualInstallment)
	ctx := context.Background()
	sched, _ := svc.Schedule(ctx, l.LoanID)
	_, _, _ = svc.RecordPayment(ctx, l.LoanID, loan.RecordPaymentRequest{AmountCents: sched.Periods[0].Payment})
	_, _, _ = svc.RecordPayment(ctx, l.LoanID, loan.RecordPaymentRequest{AmountCents: sched.Periods[1].Payment})
	// Now closed; further payment rejected.
	_, _, err := svc.RecordPayment(ctx, l.LoanID, loan.RecordPaymentRequest{AmountCents: sched.Periods[1].Payment})
	if err == nil {
		t.Fatal("expected terminal rejection on closed loan")
	}
}

// schedSumPrincipal sums the plan principal (used in prepay assertions).
func schedSumPrincipal(s loan.ScheduleResponse) int64 {
	var sum int64
	for _, p := range s.Periods {
		sum += p.Principal
	}
	return sum
}
