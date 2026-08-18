package amort

import (
	"fmt"

	"task109-loanamort/internal/domain"
)

// AccruedInterest computes the interest accrued through period `asOf` on a
// schedule, i.e. the sum of scheduled interest for periods 1..asOf. It is the
// gross interest the plan charges up to that point, independent of what has
// actually been paid.
func AccruedInterest(schedule []Period, asOf int) int64 {
	var sum int64
	for i := range schedule {
		if schedule[i].Number <= asOf {
			sum += schedule[i].Interest
		}
	}
	return sum
}

// NextUnpaidPeriod returns the 1-based number of the next unpaid scheduled
// period: paidPeriods + 1. If paidPeriods >= termPeriods the loan is fully
// scheduled (the caller should treat it as closed).
func NextUnpaidPeriod(paidPeriods, termPeriods int) int {
	if paidPeriods >= termPeriods {
		return termPeriods + 1 // past the end
	}
	return paidPeriods + 1
}

// SumPrincipal returns the total principal across the periods. A healthy
// schedule sums exactly to the original outstanding principal.
func SumPrincipal(periods []Period) int64 {
	var sum int64
	for i := range periods {
		sum += periods[i].Principal
	}
	return sum
}

// SumInterest returns the total interest across the periods.
func SumInterest(periods []Period) int64 {
	var sum int64
	for i := range periods {
		sum += periods[i].Interest
	}
	return sum
}

// SumPayment returns the total payment across the periods.
func SumPayment(periods []Period) int64 {
	var sum int64
	for i := range periods {
		sum += periods[i].Payment
	}
	return sum
}

// FinalBalance returns the outstanding principal after the last period. A
// healthy schedule reaches exactly zero.
func FinalBalance(periods []Period) int64 {
	if len(periods) == 0 {
		return 0
	}
	return periods[len(periods)-1].Balance
}

// BuildFixedPayment builds an equal-installment schedule over `periods`
// periods at a caller-supplied fixed payment `payment`. This is the path used
// by reduce_term and by the remaining-schedule rebuild for an equal-installment
// loan whose stored payment is authoritative: the payment is kept from the
// pre-prepay schedule and the term is shortened, so the payment is no longer
// the formula value for the new (lower) outstanding principal. Periods 1..n-1
// charge interest = round(balance × r) and principal = payment − interest; the
// final period absorbs the residual so principal sums exactly to outstanding
// and the balance zeroes.
//
// For r = 0 this reduces to principal = payment (remainder in the last period).
func BuildFixedPayment(in ScheduleInput, payment int64) ([]Period, error) {
	if in.Periods <= 0 {
		return nil, fmt.Errorf("amort: periods must be > 0, got %d", in.Periods)
	}
	if in.Outstanding < 0 {
		return nil, fmt.Errorf("amort: outstanding must be >= 0, got %d", in.Outstanding)
	}
	if in.Outstanding == 0 {
		return nil, nil
	}
	if payment <= 0 {
		return nil, fmt.Errorf("amort: payment must be > 0 for outstanding %d", in.Outstanding)
	}
	result := make([]Period, in.Periods)
	balance := in.Outstanding
	for i := 0; i < in.Periods; i++ {
		interest := domain.MulRateMicro(balance, in.PeriodicRateMicro)
		var principal int64
		if i == in.Periods-1 {
			principal = balance // tail correction: pay whatever remains
		} else {
			principal = payment - interest
		}
		pmt := principal + interest
		balance -= principal
		result[i] = Period{
			Number:    i + 1,
			Principal: principal,
			Interest:  interest,
			Payment:   pmt,
			Balance:   balance,
		}
	}
	return result, nil
}
