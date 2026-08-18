package amort

import "fmt"

// ValidateSchedule checks the post-conditions required before a schedule is
// persisted or returned to a caller. It catches negative amortization and
// rounding regressions at the boundary instead of letting them enter a ledger.
func ValidateSchedule(periods []Period, principal int64) error {
	if len(periods) == 0 && principal != 0 {
		return fmt.Errorf("empty schedule for non-zero principal")
	}
	var total int64
	for i, p := range periods {
		if p.Number != i+1 || p.Principal < 0 || p.Interest < 0 || p.Payment < 0 || p.Balance < 0 {
			return fmt.Errorf("invalid period %d", i+1)
		}
		total += p.Principal
	}
	if total != principal || FinalBalance(periods) != 0 {
		return fmt.Errorf("schedule does not close principal: total=%d final=%d want=%d", total, FinalBalance(periods), principal)
	}
	return nil
}
