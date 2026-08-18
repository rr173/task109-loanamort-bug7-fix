// Package domain holds the shared, dependency-free primitives used by both the
// amortization math (internal/amort) and the loan domain (internal/loan):
// the LoanType constants, the fixed-precision rate scale and the money helpers.
//
// Keeping these in a leaf package (no imports of any other internal package)
// is what breaks the would-be import cycle between amort and loan: amort
// depends on domain for LoanType + rounding, and loan depends on amort for the
// schedule math, but neither depends on the other.
package domain

import "math"

// LoanType selects the amortization method. See internal/amort for the math.
type LoanType string

const (
	// EqualInstallment (等额本息): fixed per-period payment, interest declines
	// and principal rises over the term.
	EqualInstallment LoanType = "equal_installment"
	// EqualPrincipal (等额本金): fixed principal per period, interest computed
	// on the remaining balance, payment declines over the term.
	EqualPrincipal LoanType = "equal_principal"
	// InterestOnly (到期还本付息): periods 1..n-1 pay interest only, period n
	// pays interest plus the full principal.
	InterestOnly LoanType = "interest_only"
)

// ValidLoanType reports whether t is a supported amortization method.
func ValidLoanType(t LoanType) bool {
	switch t {
	case EqualInstallment, EqualPrincipal, InterestOnly:
		return true
	}
	return false
}

// RateMicroScale is the fixed-precision denominator for periodic rates: a
// periodic rate is stored as rate_micro = rate × 1e6 (so 0.005 → 5000). All
// interest is computed as round(balance_cents × rate_micro / RateMicroScale),
// which keeps money in integer cents and avoids floating point in the hot
// path. The only float used is the one-time equal-installment payment formula
// (see amort.PaymentAmount); cent-level rounding drift is absorbed by the
// final period (tail correction) so total principal always zeroes exactly.
const RateMicroScale int64 = 1_000_000

// PeriodsPerYear is the number of payment periods per year (monthly). The
// periodic rate is the annual rate divided by PeriodsPerYear.
const PeriodsPerYear = 12

// RoundCents rounds a floating-point cent value half away from zero to the
// nearest integer cent. It is the single rounding primitive used across the
// engine so that interest and payments are always integer cents.
func RoundCents(x float64) int64 {
	if x >= 0 {
		return int64(math.Floor(x + 0.5))
	}
	return int64(math.Ceil(x - 0.5))
}

// MulRateMicro computes round(balance × rate_micro / RateMicroScale) using
// float64 intermediates. balance is in cents, rate_micro is the periodic rate
// scaled by RateMicroScale (1e6). The result is interest in cents.
//
// float64 is sufficient here because the inputs are exact integers well within
// float64's 53-bit mantissa (a 1e12-cent balance and a 1e6-scale rate still
// round-trip exactly), and the final result is rounded to cents — the residual
// drift across a schedule is handled by the final-period tail correction, not
// by chasing sub-cent precision.
func MulRateMicro(balance, rateMicro int64) int64 {
	if balance == 0 || rateMicro == 0 {
		return 0
	}
	return RoundCents(float64(balance) * float64(rateMicro) / float64(RateMicroScale))
}

// AnnualPercentToRateMicro converts an annual percentage rate (e.g. 6.0 for
// 6%/year) to the periodic (monthly) rate scaled by RateMicroScale.
// periodic_rate = annual_percent / 100 / PeriodsPerYear.
func AnnualPercentToRateMicro(annualPercent float64) int64 {
	periodic := annualPercent / 100.0 / float64(PeriodsPerYear)
	return RoundCents(periodic * float64(RateMicroScale))
}

// RateMicroToAnnualPercent converts a periodic rate_micro back to an annual
// percentage. It is the inverse of AnnualPercentToRateMicro up to the 1e-6
// rate precision; used only for display, never for recomputing interest.
func RateMicroToAnnualPercent(rateMicro int64) float64 {
	periodic := float64(rateMicro) / float64(RateMicroScale)
	return periodic * float64(PeriodsPerYear) * 1000.0
}

// CentsToFloat converts integer cents to a display float dollars value.
func CentsToFloat(cents int64) float64 { return float64(cents) / 100.0 }

// FloatToCents converts a display dollars value to integer cents, rounded.
func FloatToCents(dollars float64) int64 { return RoundCents(dollars * 100.0) }
