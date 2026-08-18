package store

import (
	"context"
	"path/filepath"
	"testing"

	"task109-loanamort/internal/loan"
)

// newTestStore opens a fresh SQLite file in the test temp dir.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// TestCRUDBorrowerLoan exercises the basic persistence loop: insert a borrower
// and a loan, then read them back.
func TestCRUDBorrowerLoan(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	tx, err := st.BeginTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := st.NextSeq(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	b := &loan.Borrower{BorrowerID: "b1", Name: "alice", CreatedSeq: seq}
	if err := st.InsertBorrower(ctx, tx, b); err != nil {
		t.Fatal(err)
	}
	lseq, _ := st.NextSeq(ctx, tx)
	l := &loan.Loan{
		LoanID: "l1", BorrowerID: "b1", Principal: 1_000_000,
		AnnualPercent: 12.0, OriginalRateMicro: 10_000, CurrentRateMicro: 10_000,
		OriginalN: 12, TermPeriods: 12, Type: loan.EqualInstallment,
		Status: loan.StatusActive, CreatedSeq: lseq,
	}
	if err := st.InsertLoan(ctx, tx, l); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Read back.
	tx2, _ := st.BeginTx(ctx)
	got, found, err := st.GetBorrower(ctx, tx2, "b1")
	if err != nil || !found {
		t.Fatalf("GetBorrower: %v found=%v", err, found)
	}
	if got.Name != "alice" {
		t.Errorf("borrower name = %q", got.Name)
	}
	gl, gfound, err := st.GetLoan(ctx, tx2, "l1")
	if err != nil || !gfound {
		t.Fatalf("GetLoan: %v found=%v", err, gfound)
	}
	if gl.Principal != 1_000_000 || gl.TermPeriods != 12 {
		t.Errorf("loan principal=%d term=%d", gl.Principal, gl.TermPeriods)
	}
	tx2.Rollback()
}

// TestNextSeqMonotonic: the sequence counter is monotonic within a transaction
// and across commits.
func TestNextSeqMonotonic(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	tx, _ := st.BeginTx(ctx)
	s1, _ := st.NextSeq(ctx, tx)
	s2, _ := st.NextSeq(ctx, tx)
	if s2 <= s1 {
		t.Errorf("seq not monotonic in-tx: %d then %d", s1, s2)
	}
	tx.Commit()

	tx2, _ := st.BeginTx(ctx)
	s3, _ := st.NextSeq(ctx, tx2)
	tx2.Commit()
	if s3 <= s2 {
		t.Errorf("seq not monotonic across commits: %d then %d", s2, s3)
	}
}

// TestNextSeqRollbackDoesNotConsume: a rolled-back transaction does not advance
// the persisted counter, so subsequent inserts are not left with a gap.
func TestNextSeqRollbackDoesNotConsume(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	// Baseline committed seq.
	tx, _ := st.BeginTx(ctx)
	before, _ := st.NextSeq(ctx, tx)
	tx.Commit()

	// A rolled-back tx that bumps the counter.
	tx2, _ := st.BeginTx(ctx)
	_, _ = st.NextSeq(ctx, tx2)
	tx2.Rollback()

	// The next committed seq should be before+1 (the rollback left no gap).
	tx3, _ := st.BeginTx(ctx)
	after, _ := st.NextSeq(ctx, tx3)
	tx3.Commit()
	if after != before+1 {
		t.Errorf("rollback consumed seq: before=%d after=%d, want %d", before, after, before+1)
	}
}

// TestInsertPaymentAndSumPrincipalPaid: the as-of balance formula reads from the
// payment ledger; insert two payments and verify SumPrincipalPaid with and
// without an as-of cutoff.
func TestInsertPaymentAndSumPrincipalPaid(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	tx, _ := st.BeginTx(ctx)
	_ = st.InsertBorrower(ctx, tx, &loan.Borrower{BorrowerID: "b", Name: "x", CreatedSeq: 1})
	_ = st.InsertLoan(ctx, tx, &loan.Loan{LoanID: "l", BorrowerID: "b", Principal: 1_000_000, CurrentRateMicro: 10_000, OriginalN: 12, TermPeriods: 12, Type: loan.EqualInstallment, Status: loan.StatusActive, CreatedSeq: 2})
	_ = st.InsertPayment(ctx, tx, &loan.Payment{PaymentID: "p1", LoanID: "l", Seq: 1, Amount: 88849, Principal: 78849, Interest: 10000, Type: loan.PaymentScheduled, CreatedSeq: 3})
	_ = st.InsertPayment(ctx, tx, &loan.Payment{PaymentID: "p2", LoanID: "l", Seq: 2, Amount: 88849, Principal: 79637, Interest: 9212, Type: loan.PaymentScheduled, CreatedSeq: 4})
	tx.Commit()

	tx2, _ := st.BeginTx(ctx)
	all, err := st.SumPrincipalPaid(ctx, tx2, "l", nil)
	if err != nil || all != 78849+79637 {
		t.Fatalf("SumPrincipalPaid(all) = %d err=%v, want %d", all, err, 78849+79637)
	}
	asOf1 := 1
	partial, err := st.SumPrincipalPaid(ctx, tx2, "l", &asOf1)
	if err != nil || partial != 78849 {
		t.Errorf("SumPrincipalPaid(as_of=1) = %d err=%v, want 78849", partial, err)
	}
	cnt, _ := st.CountScheduledPaid(ctx, tx2, "l")
	if cnt != 2 {
		t.Errorf("CountScheduledPaid = %d, want 2", cnt)
	}
	tx2.Rollback()
}

// TestListLoansFilter: the status/type/borrower filters narrow results.
func TestListLoansFilter(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	tx, _ := st.BeginTx(ctx)
	_ = st.InsertBorrower(ctx, tx, &loan.Borrower{BorrowerID: "b", Name: "x", CreatedSeq: 1})
	for i, id := range []string{"l1", "l2", "l3"} {
		_ = st.InsertLoan(ctx, tx, &loan.Loan{LoanID: id, BorrowerID: "b", Principal: 1000, CurrentRateMicro: 100, OriginalN: 6, TermPeriods: 6, Type: loanType(i), Status: loan.StatusActive, CreatedSeq: int64(i + 2)})
	}
	tx.Commit()

	tx2, _ := st.BeginTx(ctx)
	active, _ := st.ListLoans(ctx, tx2, loan.ListLoansFilter{Status: loan.StatusActive})
	if len(active) != 3 {
		t.Errorf("active loans = %d, want 3", len(active))
	}
	ei, _ := st.ListLoans(ctx, tx2, loan.ListLoansFilter{Type: loan.EqualInstallment})
	if len(ei) != 1 {
		t.Errorf("equal_installment loans = %d, want 1", len(ei))
	}
	tx2.Rollback()
}

// TestRestartRecovery: data written in one connection survives a close+reopen
// on the same file (the restart-recovery path).
func TestRestartRecovery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "recover.db")

	st1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tx, _ := st1.BeginTx(ctx)
	_ = st1.InsertBorrower(ctx, tx, &loan.Borrower{BorrowerID: "b", Name: "persisted", CreatedSeq: 1})
	_ = st1.InsertLoan(ctx, tx, &loan.Loan{LoanID: "l", BorrowerID: "b", Principal: 1_000_000, CurrentRateMicro: 10_000, OriginalN: 12, TermPeriods: 12, Type: loan.EqualInstallment, Status: loan.StatusActive, CreatedSeq: 2})
	tx.Commit()
	st1.Close()

	// Reopen the same file.
	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	tx2, _ := st2.BeginTx(ctx)
	b, found, _ := st2.GetBorrower(ctx, tx2, "b")
	if !found || b.Name != "persisted" {
		t.Fatalf("borrower not recovered: found=%v name=%q", found, b.Name)
	}
	l, lfound, _ := st2.GetLoan(ctx, tx2, "l")
	if !lfound || l.Principal != 1_000_000 {
		t.Fatalf("loan not recovered: found=%v principal=%d", lfound, l.Principal)
	}
	tx2.Rollback()
}

// loanType picks a distinct type per index.
func loanType(i int) loan.LoanType {
	switch i % 3 {
	case 0:
		return loan.EqualInstallment
	case 1:
		return loan.EqualPrincipal
	default:
		return loan.InterestOnly
	}
}
