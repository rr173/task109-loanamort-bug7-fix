package amort

import (
	"testing"

	"task109-loanamort/internal/domain"
)

func TestOutstandingFromLedgerReducesByPrincipal(t *testing.T) {
	// Stand in for loan.Payment using a minimal local type that matches the
	// shape the ledger expects (the real type lives in the loan package; the
	// pure function operates on the same struct via the loan package's
	// outstandingFromLedger, tested there). Here we exercise the Sum/Final
	// helpers that the schedule endpoint relies on.
	periods, err := Build(ScheduleInput{Outstanding: 1_000_000, PeriodicRateMicro: 10_000, Periods: 12, Type: domain.EqualInstallment})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := SumPrincipal(periods); got != 1_000_000 {
		t.Errorf("SumPrincipal = %d, want 1000000", got)
	}
	if got := FinalBalance(periods); got != 0 {
		t.Errorf("FinalBalance = %d, want 0", got)
	}
	// SumPayment == SumPrincipal + SumInterest (invariant).
	if SumPayment(periods) != SumPrincipal(periods)+SumInterest(periods) {
		t.Errorf("SumPayment invariant broken")
	}
}

func TestFinalBalanceEmpty(t *testing.T) {
	if got := FinalBalance(nil); got != 0 {
		t.Errorf("FinalBalance(nil) = %d, want 0", got)
	}
}

func TestBuildAtFixedPaymentZeroesAndKeepsPayment(t *testing.T) {
	// 700000 outstanding, 1%/mo, kept payment 88849 (the original 1,000,000/12
	// payment) — solve over a fixed payment and confirm zeroing.
	const outstanding int64 = 700_000
	periods, err := BuildFixedPayment(ScheduleInput{Outstanding: outstanding, PeriodicRateMicro: 10_000, Periods: 9, Type: domain.EqualInstallment}, 88849)
	if err != nil {
		t.Fatalf("BuildFixedPayment: %v", err)
	}
	if got := SumPrincipal(periods); got != outstanding {
		t.Errorf("sum principal %d != %d", got, outstanding)
	}
	if got := FinalBalance(periods); got != 0 {
		t.Errorf("final balance %d != 0", got)
	}
	// All but the last period use the kept payment.
	for i := 0; i < len(periods)-1; i++ {
		if periods[i].Payment != 88849 {
			t.Errorf("period %d payment %d != 88849", i+1, periods[i].Payment)
		}
	}
}

func TestBuildAtFixedPaymentRejectsTooSmallPayment(t *testing.T) {
	// A payment below first-period interest must be rejected.
	_, err := BuildFixedPayment(ScheduleInput{Outstanding: 1_000_000, PeriodicRateMicro: 10_000, Periods: 12, Type: domain.EqualInstallment}, 5_000)
	if err == nil {
		t.Fatal("expected rejection for payment below first interest")
	}
}
