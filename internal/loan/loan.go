// Package loan holds the domain types, the Store interface and the Service
// (business-logic layer) for the loan-amortization engine. It depends on the
// leaf domain package (money + LoanType) and the pure amort math
// (internal/amort); nothing imports loan except the HTTP layer and main.
package loan

import "task109-loanamort/internal/domain"

// LoanType is the amortization method, re-exported from the domain leaf so the
// HTTP layer and callers refer to it as loan.LoanType.
type LoanType = domain.LoanType

// Re-export the loan-type constants so existing code that references
// loan.EqualInstallment etc. keeps working.
const (
	EqualInstallment = domain.EqualInstallment
	EqualPrincipal   = domain.EqualPrincipal
	InterestOnly     = domain.InterestOnly
)

// ValidLoanType reports whether t is a supported amortization method.
func ValidLoanType(t LoanType) bool { return domain.ValidLoanType(t) }

// Re-export the money helpers so the service refers to them as loan.* without
// every call site having to import domain directly.
const (
	RateMicroScale = domain.RateMicroScale
	PeriodsPerYear = domain.PeriodsPerYear
)

// RoundCents, MulRateMicro and the rate converters are thin aliases to the
// domain package.
var (
	RoundCents               = domain.RoundCents
	MulRateMicro             = domain.MulRateMicro
	AnnualPercentToRateMicro = domain.AnnualPercentToRateMicro
	RateMicroToAnnualPercent = domain.RateMicroToAnnualPercent
	CentsToFloat             = domain.CentsToFloat
	FloatToCents             = domain.FloatToCents
)

// LoanStatus is the lifecycle state of a loan.
type LoanStatus string

const (
	// StatusActive: accepting payments, prepayments and rate changes.
	StatusActive LoanStatus = "active"
	// StatusClosed: fully paid off; only reads are accepted.
	StatusClosed LoanStatus = "closed"
	// StatusCanceled: annulled before any payment; only reads are accepted.
	StatusCanceled LoanStatus = "canceled"
)

// IsTerminal reports whether a status no longer accepts mutations. Terminal
// loans (closed/canceled) reject payments, prepayments and rate changes but
// still answer balance/schedule/summary reads.
func IsTerminal(status LoanStatus) bool {
	return status == StatusClosed || status == StatusCanceled
}

// PaymentType distinguishes a scheduled period payment from an extra
// principal prepayment.
type PaymentType string

const (
	// PaymentScheduled: an on-plan payment for the next unpaid period.
	PaymentScheduled PaymentType = "scheduled"
	// PaymentPrepayment: an extra principal payment that recasts the plan.
	PaymentPrepayment PaymentType = "prepayment"
)

// PrepayStrategy selects how a prepayment recasts the remaining schedule.
type PrepayStrategy string

const (
	// ReduceTerm (缩期): keep the per-period payment, solve for a shorter
	// remaining term.
	ReduceTerm PrepayStrategy = "reduce_term"
	// ReducePayment (减额): keep the remaining term, recompute a lower
	// per-period payment.
	ReducePayment PrepayStrategy = "reduce_payment"
)

// ValidPrepayStrategy reports whether s is a supported recast strategy.
func ValidPrepayStrategy(s PrepayStrategy) bool {
	return s == ReduceTerm || s == ReducePayment
}
