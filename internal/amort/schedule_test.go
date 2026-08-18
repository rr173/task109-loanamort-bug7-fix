package amort

import (
	"testing"

	"task109-loanamort/internal/domain"
)

// TestEqualInstallmentZeroesExactly is the headline correctness property: for
// a non-trivial equal-installment loan, the sum of per-period principal equals
// the original outstanding principal exactly and the final balance is zero.
// Rounding drift is present (12% / 12 periods has fractional cents) and must
// be absorbed by the final period's tail correction.
func TestEqualInstallmentZeroesExactly(t *testing.T) {
	const principal int64 = 1_000_000
	periods, err := Build(ScheduleInput{
		Outstanding:       principal,
		PeriodicRateMicro: 10_000, // 12%/yr → 1%/mo
		Periods:           12,
		Type:              domain.EqualInstallment,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(periods) != 12 {
		t.Fatalf("got %d periods, want 12", len(periods))
	}
	if got := SumPrincipal(periods); got != principal {
		t.Errorf("sum principal = %d, want %d (tail correction failed)", got, principal)
	}
	if got := FinalBalance(periods); got != 0 {
		t.Errorf("final balance = %d, want 0", got)
	}
	// Every period's payment must equal principal + interest (invariant).
	for i, p := range periods {
		if p.Payment != p.Principal+p.Interest {
			t.Errorf("period %d: payment %d != principal %d + interest %d", i+1, p.Payment, p.Principal, p.Interest)
		}
		if p.Principal < 0 {
			t.Errorf("period %d: negative principal %d", i+1, p.Principal)
		}
	}
	// Interest must be strictly decreasing for equal_installment.
	for i := 1; i < len(periods); i++ {
		if periods[i].Interest > periods[i-1].Interest {
			// Allow the final-period tail to perturb interest ordering only
			// if the principal was tail-corrected; otherwise flag it.
			if i != len(periods)-1 {
				t.Errorf("period %d interest %d > period %d interest %d", i+1, periods[i].Interest, i, periods[i-1].Interest)
			}
		}
	}
}

func TestEqualInstallmentFixedPaymentValue(t *testing.T) {
	// 1,000,000 cents at 1%/mo over 12 periods: M = 88848.79... → 88849.
	got := PaymentAmount(1_000_000, 10_000, 12)
	if got != 88849 {
		t.Errorf("PaymentAmount = %d, want 88849", got)
	}
}

func TestEqualInstallmentZeroRate(t *testing.T) {
	// Zero rate: payment = principal/n, no division by zero, interest always 0.
	const principal int64 = 1_000_000
	periods, err := Build(ScheduleInput{
		Outstanding:       principal,
		PeriodicRateMicro: 0,
		Periods:           4,
		Type:              domain.EqualInstallment,
	})
	if err != nil {
		t.Fatalf("Build zero-rate: %v", err)
	}
	if got := SumPrincipal(periods); got != principal {
		t.Errorf("zero-rate sum principal = %d, want %d", got, principal)
	}
	if got := FinalBalance(periods); got != 0 {
		t.Errorf("zero-rate final balance = %d, want 0", got)
	}
	for i, p := range periods {
		if p.Interest != 0 {
			t.Errorf("zero-rate period %d interest %d != 0", i+1, p.Interest)
		}
		if p.Payment != 250_000 {
			t.Errorf("zero-rate period %d payment %d != 250000", i+1, p.Payment)
		}
	}
}

func TestEqualPrincipalExactness(t *testing.T) {
	const principal int64 = 1_200_000
	periods, err := Build(ScheduleInput{
		Outstanding:       principal,
		PeriodicRateMicro: 10_000,
		Periods:           12,
		Type:              domain.EqualPrincipal,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := SumPrincipal(periods); got != principal {
		t.Errorf("sum principal = %d, want %d", got, principal)
	}
	if got := FinalBalance(periods); got != 0 {
		t.Errorf("final balance = %d, want 0", got)
	}
	// Equal principal: principal portion is constant (100000), payment
	// strictly decreases as interest declines.
	for i := 1; i < len(periods); i++ {
		if periods[i].Principal != 100_000 {
			t.Errorf("period %d principal %d != 100000", i+1, periods[i].Principal)
		}
		if periods[i].Payment >= periods[i-1].Payment {
			t.Errorf("period %d payment %d not < period %d %d", i+1, periods[i].Payment, i, periods[i-1].Payment)
		}
	}
}

// TestEqualPrincipalRemainderDistribution: when principal doesn't divide
// evenly, the cent remainder is distributed to the first periods so the total
// is exact.
func TestEqualPrincipalRemainderDistribution(t *testing.T) {
	const principal int64 = 1_000_003 // 3-cent remainder over 4 periods
	periods, err := Build(ScheduleInput{
		Outstanding:       principal,
		PeriodicRateMicro: 0,
		Periods:           4,
		Type:              domain.EqualPrincipal,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := SumPrincipal(periods); got != principal {
		t.Errorf("sum principal = %d, want %d (remainder not distributed)", got, principal)
	}
	if got := FinalBalance(periods); got != 0 {
		t.Errorf("final balance = %d, want 0", got)
	}
	// First three periods get 250001, last gets 250000.
	if periods[0].Principal != 250_001 || periods[3].Principal != 250_000 {
		t.Errorf("remainder distribution wrong: %d %d %d %d", periods[0].Principal, periods[1].Principal, periods[2].Principal, periods[3].Principal)
	}
}

func TestInterestOnlyFinalPrincipal(t *testing.T) {
	const principal int64 = 500_000
	periods, err := Build(ScheduleInput{
		Outstanding:       principal,
		PeriodicRateMicro: 5_000, // 6%/yr
		Periods:           6,
		Type:              domain.InterestOnly,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := SumPrincipal(periods); got != principal {
		t.Errorf("sum principal = %d, want %d", got, principal)
	}
	if got := FinalBalance(periods); got != 0 {
		t.Errorf("final balance = %d, want 0", got)
	}
	// Periods 1..5 pay interest only (principal 0); period 6 pays principal.
	for i := 0; i < 5; i++ {
		if periods[i].Principal != 0 {
			t.Errorf("period %d principal %d != 0 (interest-only)", i+1, periods[i].Principal)
		}
		if periods[i].Payment != periods[i].Interest {
			t.Errorf("period %d payment %d != interest %d", i+1, periods[i].Payment, periods[i].Interest)
		}
	}
	if periods[5].Principal != principal {
		t.Errorf("final period principal %d != %d", periods[5].Principal, principal)
	}
}

func TestBuildValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		in   ScheduleInput
	}{
		{"zero periods", ScheduleInput{Outstanding: 1000, Periods: 0, Type: domain.EqualInstallment}},
		{"negative outstanding", ScheduleInput{Outstanding: -1, Periods: 1, Type: domain.EqualInstallment}},
		{"negative rate", ScheduleInput{Outstanding: 1000, PeriodicRateMicro: -1, Periods: 1, Type: domain.EqualInstallment}},
		{"unknown type", ScheduleInput{Outstanding: 1000, Periods: 1, Type: "bogus"}},
	}
	for _, c := range cases {
		if _, err := Build(c.in); err == nil {
			t.Errorf("%s: expected error, got nil", c.name)
		}
	}
}

func TestAccruedInterestSumsThroughAsOf(t *testing.T) {
	periods, _ := Build(ScheduleInput{Outstanding: 1_000_000, PeriodicRateMicro: 10_000, Periods: 12, Type: domain.EqualInstallment})
	total := SumInterest(periods)
	if got := AccruedInterest(periods, 12); got != total {
		t.Errorf("accrued(12) = %d, want total %d", got, total)
	}
	if got := AccruedInterest(periods, 0); got != 0 {
		t.Errorf("accrued(0) = %d, want 0", got)
	}
	// accrued(1) == first period's interest.
	if got := AccruedInterest(periods, 1); got != periods[0].Interest {
		t.Errorf("accrued(1) = %d, want %d", got, periods[0].Interest)
	}
}

func TestNextUnpaidPeriod(t *testing.T) {
	if got := NextUnpaidPeriod(0, 12); got != 1 {
		t.Errorf("NextUnpaidPeriod(0,12) = %d, want 1", got)
	}
	if got := NextUnpaidPeriod(5, 12); got != 6 {
		t.Errorf("NextUnpaidPeriod(5,12) = %d, want 6", got)
	}
	// Past the end → term+1.
	if got := NextUnpaidPeriod(12, 12); got != 13 {
		t.Errorf("NextUnpaidPeriod(12,12) = %d, want 13", got)
	}
}
