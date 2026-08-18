package domain

import "testing"

func TestRoundCents(t *testing.T) {
	cases := []struct {
		in   float64
		want int64
	}{
		{0, 0}, {0.4, 0}, {0.5, 1}, {0.6, 1}, {1.5, 2}, {2.5, 3},
		{-0.5, -1}, {-0.4, 0}, {-1.5, -2}, {100.0, 100},
	}
	for _, c := range cases {
		if got := RoundCents(c.in); got != c.want {
			t.Errorf("RoundCents(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestMulRateMicro(t *testing.T) {
	// 1,000,000 cents at periodic rate 0.005 (5000 micro) → interest 5000 cents.
	if got := MulRateMicro(1_000_000, 5000); got != 5000 {
		t.Errorf("interest = %d, want 5000", got)
	}
	// Zero rate or zero balance → zero interest.
	if got := MulRateMicro(1_000_000, 0); got != 0 {
		t.Errorf("zero rate interest = %d, want 0", got)
	}
	if got := MulRateMicro(0, 5000); got != 0 {
		t.Errorf("zero balance interest = %d, want 0", got)
	}
	// Rounding: 921151 × 0.005 = 4605.755 → 4606.
	if got := MulRateMicro(921151, 5000); got != 4606 {
		t.Errorf("rounded interest = %d, want 4606", got)
	}
}

func TestAnnualPercentToRateMicro(t *testing.T) {
	cases := []struct {
		annual float64
		want   int64
	}{
		{0.0, 0},    // 0% → 0 micro
		{6.0, 5000}, // 6%/yr → 0.005/mo → 5000 micro
		{12.0, 10000},
		{3.6, 3000}, // 3.6%/yr → 0.003/mo → 3000 micro
	}
	for _, c := range cases {
		if got := AnnualPercentToRateMicro(c.annual); got != c.want {
			t.Errorf("AnnualPercentToRateMicro(%v) = %d, want %d", c.annual, got, c.want)
		}
	}
}

func TestRateMicroToAnnualPercentRoundTrip(t *testing.T) {
	// For rates that divide evenly (0%, 6%, 12%) the round-trip is exact;
	// 3.6% lands at 3.6000...005 due to float64, so allow a tiny tolerance.
	for _, annual := range []float64{0, 6.0, 12.0} {
		micro := AnnualPercentToRateMicro(annual)
		back := RateMicroToAnnualPercent(micro)
		if back != annual {
			t.Errorf("round-trip %v → micro %d → %v (mismatch)", annual, micro, back)
		}
	}
	// 3.6%: micro 3000 → 3.6000...005; within 1e-6 is the rate precision.
	back := RateMicroToAnnualPercent(AnnualPercentToRateMicro(3.6))
	if abs(back-3.6) > 1e-6 {
		t.Errorf("3.6%% round-trip %v exceeds tolerance", back)
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func TestValidLoanType(t *testing.T) {
	for _, lt := range []LoanType{EqualInstallment, EqualPrincipal, InterestOnly} {
		if !ValidLoanType(lt) {
			t.Errorf("ValidLoanType(%q) = false, want true", lt)
		}
	}
	for _, lt := range []LoanType{"", "bogus", " EqualInstallment"} {
		if ValidLoanType(lt) {
			t.Errorf("ValidLoanType(%q) = true, want false", lt)
		}
	}
}

func TestCentsFloatConversions(t *testing.T) {
	if got := CentsToFloat(12345); got != 123.45 {
		t.Errorf("CentsToFloat(12345) = %v, want 123.45", got)
	}
	if got := FloatToCents(123.45); got != 12345 {
		t.Errorf("FloatToCents(123.45) = %d, want 12345", got)
	}
}
