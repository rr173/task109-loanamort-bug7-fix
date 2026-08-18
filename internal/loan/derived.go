package loan

import (
	"task109-loanamort/internal/amort"
)

// outstandingFromLedger computes the outstanding principal at as-of period
// `asOf` from a payment ledger: Principal − Σ(payment.Principal where
// payment.Seq ≤ asOf). This is the restart-recovery formula — it never reads
// a cached balance field, only the append-only payment rows.
//
// If asOf < 0, all payments are summed (the current outstanding).
func outstandingFromLedger(principal int64, payments []Payment, asOf int) int64 {
	if asOf < 0 {
		// Include every payment regardless of seq.
		var paid int64
		for i := range payments {
			paid += payments[i].Principal
		}
		return principal - paid
	}
	var paid int64
	for i := range payments {
		if payments[i].Seq <= asOf {
			paid += payments[i].Principal
		}
	}
	return principal - paid
}

// remainingSchedule rebuilds the unpaid portion of the schedule from the
// current loan state and the ledger-derived outstanding principal. It is the
// single source of truth for "what does the borrower still owe per period":
// the schedule endpoint, the next-payment check and the projection all flow
// from it.
//
// For equal_installment the per-period payment is the loan's stored
// CurrentPayment (set at creation, kept across scheduled payments and
// reduce_term, recomputed on reduce_payment/rate-change). Recomputing the
// formula from the current outstanding + remaining periods would drift by a
// cent from the stored payment because each period's interest is rounded
// independently, so the stored payment is authoritative.
//
// Period numbers are ABSOLUTE (paidPeriods + i + 1), not relative to the
// remaining slice. This is critical: RecordPayment stamps a payment's Seq from
// the next unpaid period's Number, and as-of balances sum by Seq. If the
// rebuilt plan restarted numbering at 1, the second payment would collide
// with the first and as_of queries would double-count.
func remainingSchedule(l Loan, outstanding int64, paidPeriods int) ([]amort.Period, error) {
	remaining := l.TermPeriods - paidPeriods
	if remaining <= 0 || outstanding <= 0 {
		return nil, nil
	}
	var periods []amort.Period
	var err error
	if l.CurrentPayment > 0 {
		// Keep the stored fixed payment; the final period absorbs tail drift.
		periods, err = amort.BuildFixedPayment(amort.ScheduleInput{
			Outstanding:       outstanding,
			PeriodicRateMicro: l.CurrentRateMicro,
			Periods:           remaining,
			Type:              l.Type,
		}, l.CurrentPayment)
	} else {
		periods, err = amort.Build(amort.ScheduleInput{
			Outstanding:       outstanding,
			PeriodicRateMicro: l.CurrentRateMicro,
			Periods:           remaining,
			Type:              l.Type,
		})
	}
	if err != nil {
		return nil, err
	}
	// Shift period numbers to absolute positions (1-based across the whole
	// term), so the next unpaid period's Number == paidPeriods + 1.
	for i := range periods {
		periods[i].Number = i + 1
	}
	return periods, nil
}
