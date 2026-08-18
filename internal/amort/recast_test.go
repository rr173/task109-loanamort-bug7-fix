package amort

import (
	"testing"

	"task109-loanamort/internal/domain"
)

// TestRecastReduceTermShortensAndZeroes: after a 200000-cent prepayment on a
// 1,000,000-cent 12-period equal-installment loan (one period paid), the new
// term is strictly shorter than 12 and the rebuilt schedule sums to the
// post-prepay outstanding and zeroes exactly.
func TestRecastReduceTermShortensAndZeroes(t *testing.T) {
	const principal int64 = 1_000_000
	// Original schedule to find period-1 principal.
	orig, err := Build(ScheduleInput{Outstanding: principal, PeriodicRateMicro: 10_000, Periods: 12, Type: domain.EqualInstallment})
	if err != nil {
		t.Fatalf("Build orig: %v", err)
	}
	paid1 := orig[0].Principal
	// Current payment (what the borrower is on) = PaymentAmount over the
	// pre-prepay outstanding and the remaining 12 periods.
	currentPayment := PaymentAmount(principal, 10_000, 12)
	outstandingAfter := principal - paid1 - 200_000
	res, err := RecastReduceTerm(RecastInput{
		Type:              domain.EqualInstallment,
		Outstanding:       outstandingAfter,
		PeriodicRateMicro: 10_000,
		PaidPeriods:       1,
		OriginalTerm:      12,
		CurrentPayment:    currentPayment,
	})
	if err != nil {
		t.Fatalf("RecastReduceTerm: %v", err)
	}
	// Term must be shorter than 12 and longer than the 1 paid period.
	if res.TermPeriods <= 1 || res.TermPeriods >= 12 {
		t.Errorf("term %d not in (1,12)", res.TermPeriods)
	}
	if res.Schedule == nil {
		t.Fatal("nil schedule")
	}
	if got := SumPrincipal(res.Schedule); got != outstandingAfter {
		t.Errorf("recast sum principal %d != outstanding %d", got, outstandingAfter)
	}
	if got := FinalBalance(res.Schedule); got != 0 {
		t.Errorf("recast final balance %d != 0", got)
	}
	// Periods 1..n-1 use the kept payment; final period tail-corrected.
	for i := 0; i < len(res.Schedule)-1; i++ {
		if res.Schedule[i].Payment != currentPayment {
			t.Errorf("period %d payment %d != kept %d", i+1, res.Schedule[i].Payment, currentPayment)
		}
	}
}

func TestRecastReduceTermFullyPrepaid(t *testing.T) {
	// Prepaying the entire outstanding → no remaining periods.
	res, err := RecastReduceTerm(RecastInput{
		Type:           domain.EqualInstallment,
		Outstanding:    0,
		PaidPeriods:    5,
		OriginalTerm:   12,
		CurrentPayment: 100,
	})
	if err != nil {
		t.Fatalf("RecastReduceTerm full: %v", err)
	}
	if res.TermPeriods != 5 || res.Schedule != nil {
		t.Errorf("full prepay: term=%d sched=%v, want 5/nil", res.TermPeriods, res.Schedule)
	}
}

func TestRecastReducePaymentKeepsTermLowersPayment(t *testing.T) {
	const principal int64 = 1_000_000
	orig, _ := Build(ScheduleInput{Outstanding: principal, PeriodicRateMicro: 10_000, Periods: 12, Type: domain.EqualInstallment})
	paid1 := orig[0].Principal
	outstandingAfter := principal - paid1 - 300_000
	res, err := RecastReducePayment(RecastInput{
		Type:              domain.EqualInstallment,
		Outstanding:       outstandingAfter,
		PeriodicRateMicro: 10_000,
		PaidPeriods:       1,
		OriginalTerm:      12,
	})
	if err != nil {
		t.Fatalf("RecastReducePayment: %v", err)
	}
	// Term unchanged at 12.
	if res.TermPeriods != 12 {
		t.Errorf("term %d != 12", res.TermPeriods)
	}
	// Remaining periods = 11.
	if len(res.Schedule) != 11 {
		t.Errorf("remaining periods %d != 11", len(res.Schedule))
	}
	// New payment strictly lower than the original.
	newPayment := res.Schedule[0].Payment
	if newPayment >= orig[1].Payment {
		t.Errorf("new payment %d not lower than original %d", newPayment, orig[1].Payment)
	}
	if got := SumPrincipal(res.Schedule); got != outstandingAfter {
		t.Errorf("recast sum principal %d != outstanding %d", got, outstandingAfter)
	}
	if got := FinalBalance(res.Schedule); got != 0 {
		t.Errorf("recast final balance %d != 0", got)
	}
}

func TestRecastRateChangeAtNewRate(t *testing.T) {
	const principal int64 = 1_000_000
	// One period paid at 6%, then refinance to 18%.
	orig, _ := Build(ScheduleInput{Outstanding: principal, PeriodicRateMicro: 5_000, Periods: 12, Type: domain.EqualInstallment})
	paid1 := orig[0].Principal
	outstandingAfter := principal - paid1
	res, err := RecastRateChange(RecastInput{
		Type:              domain.EqualInstallment,
		Outstanding:       outstandingAfter,
		PeriodicRateMicro: 15_000, // 18%/yr
		PaidPeriods:       1,
		OriginalTerm:      12,
	})
	if err != nil {
		t.Fatalf("RecastRateChange: %v", err)
	}
	if res.TermPeriods != 12 {
		t.Errorf("term %d != 12 (rate change keeps term)", res.TermPeriods)
	}
	if len(res.Schedule) != 11 {
		t.Errorf("remaining %d != 11", len(res.Schedule))
	}
	// New payment at the higher rate must be higher than the original.
	newPayment := res.Schedule[0].Payment
	if newPayment <= orig[1].Payment {
		t.Errorf("higher-rate payment %d not > original %d", newPayment, orig[1].Payment)
	}
	if got := SumPrincipal(res.Schedule); got != outstandingAfter {
		t.Errorf("sum principal %d != %d", got, outstandingAfter)
	}
	if got := FinalBalance(res.Schedule); got != 0 {
		t.Errorf("final balance %d != 0", got)
	}
}

func TestRecastReducePaymentZeroOutstanding(t *testing.T) {
	res, err := RecastReducePayment(RecastInput{
		Type:         domain.EqualInstallment,
		Outstanding:  0,
		PaidPeriods:  5,
		OriginalTerm: 12,
	})
	if err != nil {
		t.Fatalf("RecastReducePayment zero: %v", err)
	}
	if res.Schedule != nil {
		t.Errorf("expected nil schedule for zero outstanding, got %d periods", len(res.Schedule))
	}
}

func TestRecastRateChangeNoRemainingPeriods(t *testing.T) {
	// Paid all periods but outstanding > 0 → error (shouldn't happen for a
	// healthy loan, but the guard must fire).
	_, err := RecastRateChange(RecastInput{
		Type:         domain.EqualInstallment,
		Outstanding:  1000,
		PaidPeriods:  12,
		OriginalTerm: 12,
	})
	if err == nil {
		t.Fatal("expected error when no remaining periods and outstanding > 0")
	}
}
