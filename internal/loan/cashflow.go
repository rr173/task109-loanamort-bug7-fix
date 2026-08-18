package loan

import "context"

// CashflowResponse combines the rebuilt future schedule with the immutable
// payment ledger, making a restart-safe cashflow view available to clients.
type CashflowResponse struct {
	LoanID    string                       `json:"loan_id"`
	Future    ScheduleResponse             `json:"future"`
	Ledger    map[PaymentType]LedgerTotals `json:"ledger"`
	Remaining int64                        `json:"remaining_principal_cents"`
}

func (s *Service) Cashflow(ctx context.Context, id string) (CashflowResponse, error) {
	future, err := s.Schedule(ctx, id)
	if err != nil {
		return CashflowResponse{}, err
	}
	balance, err := s.Balance(ctx, id, -1)
	if err != nil {
		return CashflowResponse{}, err
	}
	ledger, err := s.LedgerBreakdown(ctx, id)
	if err != nil {
		return CashflowResponse{}, err
	}
	return CashflowResponse{LoanID: id, Future: future, Ledger: ledger, Remaining: future.TotalPrincipal + balance.Outstanding}, nil
}
