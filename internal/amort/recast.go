package amort

import (
	"fmt"
	"math"

	"task109-loanamort/internal/domain"
)

// RecastResult describes a schedule recast after a prepayment or rate change.
// It returns the new term (periods) and the rebuilt remaining schedule. The
// caller (service) persists TermPeriods = res.TermPeriods for reduce_term, or
// keeps TermPeriods unchanged for reduce_payment/rate_change.
type RecastResult struct {
	TermPeriods int      // total term after recast (paid + remaining)
	Schedule    []Period // remaining (unpaid) periods
}

// RecastInput is the state at the moment of recast.
type RecastInput struct {
	Type              domain.LoanType
	Outstanding       int64 // principal still owed AFTER the prepayment, if any
	PeriodicRateMicro int64 // current periodic rate (after a rate change, if any)
	PaidPeriods       int   // how many scheduled periods are already paid
	OriginalTerm      int   // total term (reduce_payment/rate_change use remaining = this − paid)
	// CurrentPayment is the per-period payment the borrower is currently on
	// (computed from the outstanding BEFORE the prepayment and the remaining
	// periods). reduce_term keeps this payment fixed and solves for a shorter
	// term; reduce_payment/rate_change ignore it and recompute.
	CurrentPayment int64
}

// RecastReduceTerm keeps the borrower's current per-period payment fixed and
// solves for the shorter remaining term that amortizes the new (post-prepay)
// outstanding principal.
//
// For r > 0 the remaining term m solves  M = B·r·(1+r)^m / ((1+r)^m − 1):
//
//	m = -log(1 − B·r/M) / log(1+r)
//
// rounded up. For r = 0, m = ceil(B / M). The schedule is then rebuilt over m
// periods at the kept payment M; the final period is tail-corrected so
// principal sums exactly to B.
func RecastReduceTerm(in RecastInput) (RecastResult, error) {
	if in.Outstanding <= 0 {
		// Fully prepaid: no remaining periods, loan closes this period.
		return RecastResult{TermPeriods: in.PaidPeriods, Schedule: nil}, nil
	}
	payment := in.CurrentPayment
	if payment <= 0 {
		return RecastResult{}, fmt.Errorf("reduce_term: current payment not provided")
	}
	var m int
	if in.PeriodicRateMicro == 0 {
		m = int(math.Ceil(float64(in.Outstanding) / float64(payment)))
	} else {
		r := float64(in.PeriodicRateMicro) / float64(domain.RateMicroScale)
		denom := 1 - float64(in.Outstanding)*r/float64(payment)
		if denom <= 0 {
			// Payment doesn't even cover perpetual interest; refuse.
			return RecastResult{}, fmt.Errorf("reduce_term: payment %d too small to amortize outstanding %d at rate %d", payment, in.Outstanding, in.PeriodicRateMicro)
		}
		m = int(math.Ceil(-math.Log(denom) / math.Log(1+r)))
	}
	if m < 1 {
		m = 1
	}
	sched, err := BuildFixedPayment(ScheduleInput{
		Outstanding:       in.Outstanding,
		PeriodicRateMicro: in.PeriodicRateMicro,
		Periods:           m,
		Type:              in.Type,
	}, payment)
	if err != nil {
		return RecastResult{}, fmt.Errorf("reduce_term: %w", err)
	}
	return RecastResult{TermPeriods: in.PaidPeriods + m, Schedule: sched}, nil
}

// RecastReducePayment keeps the remaining term fixed and recomputes a lower
// per-period payment over the new outstanding principal. The remaining periods
// = OriginalTerm − PaidPeriods.
func RecastReducePayment(in RecastInput) (RecastResult, error) {
	remaining := in.OriginalTerm
	if remaining < 0 {
		return RecastResult{}, fmt.Errorf("reduce_payment: paid %d exceeds term %d", in.PaidPeriods, in.OriginalTerm)
	}
	if in.Outstanding <= 0 {
		return RecastResult{TermPeriods: in.PaidPeriods + remaining, Schedule: nil}, nil
	}
	if remaining == 0 {
		return RecastResult{}, fmt.Errorf("reduce_payment: no remaining periods but outstanding %d > 0", in.Outstanding)
	}
	sched, err := Build(ScheduleInput{
		Outstanding:       in.Outstanding,
		PeriodicRateMicro: in.PeriodicRateMicro,
		Periods:           remaining,
		Type:              in.Type,
	})
	if err != nil {
		return RecastResult{}, fmt.Errorf("reduce_payment: %w", err)
	}
	return RecastResult{TermPeriods: in.OriginalTerm, Schedule: sched}, nil
}

// RecastRateChange recomputes the schedule over the remaining term at a new
// periodic rate. It behaves like reduce_payment (term unchanged) but with the
// new rate. Past payments are immutable.
func RecastRateChange(in RecastInput) (RecastResult, error) {
	remaining := in.OriginalTerm - in.PaidPeriods + 1
	if remaining < 0 {
		return RecastResult{}, fmt.Errorf("rate_change: paid %d exceeds term %d", in.PaidPeriods, in.OriginalTerm)
	}
	if in.Outstanding <= 0 {
		return RecastResult{TermPeriods: in.OriginalTerm, Schedule: nil}, nil
	}
	if remaining == 0 {
		return RecastResult{}, fmt.Errorf("rate_change: no remaining periods but outstanding %d > 0", in.Outstanding)
	}
	sched, err := Build(ScheduleInput{
		Outstanding:       in.Outstanding,
		PeriodicRateMicro: in.PeriodicRateMicro,
		Periods:           remaining,
		Type:              in.Type,
	})
	if err != nil {
		return RecastResult{}, fmt.Errorf("rate_change: %w", err)
	}
	return RecastResult{TermPeriods: in.OriginalTerm, Schedule: sched}, nil
}
