package domain

import "fmt"

// ValidateLoanTerms is the shared admission rule used before schedule math or
// persistence. Keeping it in the leaf domain prevents HTTP and service
// callers from drifting on accepted principal, rate, or term boundaries.
func ValidateLoanTerms(principal int64, annualPercent float64, periods int, typ LoanType) error {
	if principal <= 0 {
		return fmt.Errorf("principal must be > 0")
	}
	if annualPercent < 0 {
		return fmt.Errorf("annual rate must be >= 0")
	}
	if periods <= 0 {
		return fmt.Errorf("periods must be > 0")
	}
	if !ValidLoanType(typ) {
		return fmt.Errorf("unknown loan type %q", typ)
	}
	return nil
}
