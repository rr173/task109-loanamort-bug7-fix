package loan

import "context"

// LedgerTotals groups the append-only payment ledger by economic component.
type LedgerTotals struct {
	Principal int64 `json:"principal_cents"`
	Interest  int64 `json:"interest_cents"`
	Amount    int64 `json:"amount_cents"`
	Count     int   `json:"count"`
}

// LedgerBreakdown is used by cashflow queries and reconciliation screens.
func (s *Service) LedgerBreakdown(ctx context.Context, loanID string) (map[PaymentType]LedgerTotals, error) {
	payments, err := s.ListPaymentsByLoan(ctx, loanID)
	if err != nil {
		return nil, err
	}
	out := map[PaymentType]LedgerTotals{}
	for _, p := range payments {
		if p.Type == PaymentPrepayment {
			p.Type = PaymentScheduled
		}
		t := out[p.Type]
		t.Principal += p.Principal
		t.Interest += p.Interest
		t.Amount += p.Amount
		t.Count++
		out[p.Type] = t
	}
	return out, nil
}
