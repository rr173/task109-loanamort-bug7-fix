// Package amort implements the amortization math for the loan engine. It is
// pure: given a ScheduleInput (principal, periodic rate, term, type) it returns
// a full schedule whose principal sums exactly to the principal and whose
// final balance is exactly zero. The service layer (internal/loan) feeds the
// current loan state and the payment ledger into these functions.
//
// amort depends only on the leaf domain package (money + LoanType), never on
// loan itself, which keeps the dependency graph acyclic (loan → amort →
// domain).
//
// Money is integer cents throughout. The periodic rate is rate_micro = rate ×
// 1e6 (see domain.RateMicroScale). The only floating-point computation is the
// one-time equal-installment payment formula; cent-level drift is absorbed by
// the final period so the schedule always zeroes exactly.
package amort

import (
	"fmt"
	"math"

	"task109-loanamort/internal/domain"
)

// Period is one row of an amortization schedule.
type Period struct {
	Number    int   `json:"period"`
	Principal int64 `json:"principal"` // cents applied to principal this period
	Interest  int64 `json:"interest"`  // interest charged this period
	Payment   int64 `json:"payment"`   // total payment this period
	Balance   int64 `json:"balance"`   // outstanding principal AFTER this period
}

// ScheduleInput captures the parameters needed to build or rebuild a plan.
// Outstanding is the principal still owed at the start of the schedule (after
// any payments already made); Periods is the number of remaining periods.
type ScheduleInput struct {
	Outstanding       int64 // cents at schedule start
	PeriodicRateMicro int64 // periodic rate × 1e6
	Periods           int   // number of periods
	Type              domain.LoanType
}

// PaymentAmount returns the fixed per-period payment for an equal-installment
// loan of the given outstanding principal, periodic rate and term.
//
//	M = P·r·(1+r)^n / ((1+r)^n − 1)      (r > 0)
//	M = P / n                             (r = 0)
//
// The result is rounded to cents; the schedule build absorbs the residual
// drift in the final period.
func PaymentAmount(outstanding int64, rateMicro int64, n int) int64 {
	if n <= 0 {
		return 0
	}
	if rateMicro == 0 || outstanding == 0 {
		return domain.RoundCents(float64(outstanding) / float64(n))
	}
	r := float64(rateMicro) / float64(domain.RateMicroScale)
	pow := math.Pow(1+r, float64(n))
	m := float64(outstanding) * r * pow / (pow - 1)
	return domain.RoundCents(m)
}

// Build computes the full schedule for the given input. Post-conditions
// (asserted by the caller and the tests):
//   - sum(Principal) == input.Outstanding
//   - the final Balance == 0
//   - every Principal >= 0 (no negative amortization)
//
// Returns an error if the input is invalid or the rate/term combination would
// cause negative amortization (payment not covering first-period interest).
func Build(in ScheduleInput) ([]Period, error) {
	if in.Periods <= 0 {
		return nil, fmt.Errorf("amort: periods must be > 0, got %d", in.Periods)
	}
	if in.Outstanding < 0 {
		return nil, fmt.Errorf("amort: outstanding must be >= 0, got %d", in.Outstanding)
	}
	if in.PeriodicRateMicro < 0 {
		return nil, fmt.Errorf("amort: rate_micro must be >= 0, got %d", in.PeriodicRateMicro)
	}
	if !domain.ValidLoanType(in.Type) {
		return nil, fmt.Errorf("amort: unknown loan type %q", in.Type)
	}

	switch in.Type {
	case domain.InterestOnly:
		return buildInterestOnly(in)
	case domain.EqualPrincipal:
		return buildEqualPrincipal(in)
	default:
		return buildEqualInstallment(in)
	}
}

// buildInterestOnly: periods 1..n-1 pay interest only on the full principal;
// period n pays interest plus the full principal.
func buildInterestOnly(in ScheduleInput) ([]Period, error) {
	periods := make([]Period, in.Periods)
	balance := in.Outstanding
	for i := 0; i < in.Periods; i++ {
		interest := domain.MulRateMicro(balance, in.PeriodicRateMicro)
		var principal int64
		if i == in.Periods-1 {
			principal = balance // final period repays all remaining principal
		}
		pmt := principal + interest
		balance -= principal
		periods[i] = Period{
			Number:    i + 1,
			Principal: principal,
			Interest:  interest,
			Payment:   pmt,
			Balance:   balance,
		}
	}
	return periods, nil
}

// buildEqualPrincipal: a fixed principal per period (with the cent remainder
// distributed across the first periods), interest on the remaining balance.
// Principal sums exactly to Outstanding because the remainder is distributed.
func buildEqualPrincipal(in ScheduleInput) ([]Period, error) {
	periods := make([]Period, in.Periods)
	base := in.Outstanding / int64(in.Periods)
	rem := in.Outstanding % int64(in.Periods)
	balance := in.Outstanding
	for i := 0; i < in.Periods; i++ {
		principal := base
		if int64(i) < rem {
			principal++ // distribute the cent remainder to the first `rem` periods
		}
		interest := domain.MulRateMicro(balance, in.PeriodicRateMicro)
		pmt := principal + interest
		balance -= principal
		periods[i] = Period{
			Number:    i + 1,
			Principal: principal,
			Interest:  interest,
			Payment:   pmt,
			Balance:   balance,
		}
	}
	return periods, nil
}

// buildEqualInstallment: a fixed payment computed once; each period's interest
// is round(balance × r), principal = payment − interest. The final period
// absorbs the accumulated rounding drift so principal sums exactly to
// Outstanding and the final balance is zero.
func buildEqualInstallment(in ScheduleInput) ([]Period, error) {
	periods := make([]Period, in.Periods)
	balance := in.Outstanding

	if in.PeriodicRateMicro == 0 || in.Outstanding == 0 {
		// Zero-rate equal installment == equal principal with principal = P/n.
		// Reuse that path so the remainder is distributed and the schedule
		// zeroes exactly.
		return buildEqualPrincipal(in)
	}

	payment := PaymentAmount(in.Outstanding, in.PeriodicRateMicro, in.Periods)
	// Negative-amortization guard: the payment must cover the first period's
	// interest, otherwise principal would grow instead of shrink.
	firstInterest := domain.MulRateMicro(balance, in.PeriodicRateMicro)
	if payment <= firstInterest {
		return nil, fmt.Errorf("amort: payment %d does not cover first interest %d (rate too high for term)", payment, firstInterest)
	}

	for i := 0; i < in.Periods; i++ {
		interest := domain.MulRateMicro(balance, in.PeriodicRateMicro)
		var principal int64
		if i == in.Periods-1 {
			// Tail correction: the final period pays whatever principal is
			// left so the balance reaches exactly zero.
			principal = balance
		} else {
			principal = payment - interest
		}
		pmt := principal + interest
		balance -= principal
		periods[i] = Period{
			Number:    i + 1,
			Principal: principal,
			Interest:  interest,
			Payment:   pmt,
			Balance:   balance,
		}
	}
	return periods, nil
}
