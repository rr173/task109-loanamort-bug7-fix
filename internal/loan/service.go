// Package loan also contains the Service, the business-logic layer that turns
// HTTP intents into transactional store operations and amortization math. The
// service depends only on the Store interface (concrete impl in internal/store)
// and the pure amort package; it never touches *sql.DB directly.
package loan

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	crand "crypto/rand"
	"encoding/hex"

	"task109-loanamort/internal/amort"
	"task109-loanamort/internal/domain"
)

// Sentinel errors returned to callers. The HTTP layer maps these to status
// codes and JSON error strings; tests assert on them directly.
var (
	// ErrNotFound: no loan/borrower/payment with that id.
	ErrNotFound = errors.New("not found")
	// ErrInvalid: malformed request (bad type, negative amount, etc).
	ErrInvalid = errors.New("invalid request")
	// ErrTerminal: the loan is closed/canceled and rejects mutations.
	ErrTerminal = errors.New("loan is terminal (closed or canceled)")
	// ErrAmountMismatch: a scheduled payment amount doesn't equal the planned
	// amount for the next unpaid period.
	ErrAmountMismatch = errors.New("payment amount does not match scheduled amount")
	// ErrPrepayTooLarge: a prepayment exceeds the outstanding principal.
	ErrPrepayTooLarge = errors.New("prepayment exceeds outstanding principal")
	// ErrConflict: a state transition precondition failed.
	ErrConflict = errors.New("state conflict")
)

// Service coordinates borrowers, loans and the payment ledger. Every mutating
// method runs inside one store transaction (BEGIN IMMEDIATE), which serializes
// writers and keeps payment + loan-state updates atomic.
type Service struct {
	store Store
}

// New returns a Service over the given store.
func New(s Store) *Service { return &Service{store: s} }

// --- borrowers ---

// CreateBorrowerRequest is the JSON body for POST /borrowers.
type CreateBorrowerRequest struct {
	Name string `json:"name"`
}

// CreateBorrower registers a borrower and returns the persisted record.
func (s *Service) CreateBorrower(ctx context.Context, req CreateBorrowerRequest) (Borrower, error) {
	name := req.Name
	if name == "" {
		return Borrower{}, fmt.Errorf("%w: name is required", ErrInvalid)
	}
	var b Borrower
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		seq, err := s.store.NextSeq(ctx, tx)
		if err != nil {
			return err
		}
		b = Borrower{
			BorrowerID: newID("brw"),
			Name:       name,
			CreatedSeq: seq,
		}
		return s.store.InsertBorrower(ctx, tx, &b)
	})
	if err != nil {
		return Borrower{}, err
	}
	return b, nil
}

// GetBorrower loads a borrower by id.
func (s *Service) GetBorrower(ctx context.Context, id string) (Borrower, error) {
	var b Borrower
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var found bool
		var err error
		b, found, err = s.store.GetBorrower(ctx, tx, id)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		return nil
	})
	return b, err
}

// ListBorrowers returns up to limit borrowers (limit<=0 → no limit).
func (s *Service) ListBorrowers(ctx context.Context, limit int) ([]Borrower, error) {
	var out []Borrower
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = s.store.ListBorrowers(ctx, tx, limit)
		return err
	})
	return out, err
}

// BorrowerLoans lists a borrower's loans.
func (s *Service) BorrowerLoans(ctx context.Context, borrowerID string) ([]Loan, error) {
	var out []Loan
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		found, err := s.borrowerExists(ctx, tx, borrowerID)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		out, err = s.store.ListLoans(ctx, tx, ListLoansFilter{BorrowerID: borrowerID})
		return err
	})
	return out, err
}

// --- loans ---

// CreateLoanRequest is the JSON body for POST /loans.
type CreateLoanRequest struct {
	BorrowerID     string   `json:"borrower_id"`
	PrincipalCents int64    `json:"principal_cents"`
	AnnualPercent  float64  `json:"annual_rate_percent"`
	Periods        int      `json:"periods"`
	Type           LoanType `json:"type"`
}

// CreateLoan validates the input, builds the schedule once (to validate the
// rate/term combination does not negative-amortize) and persists the loan.
func (s *Service) CreateLoan(ctx context.Context, req CreateLoanRequest) (Loan, error) {
	if req.BorrowerID == "" {
		return Loan{}, fmt.Errorf("%w: borrower_id is required", ErrInvalid)
	}
	if err := domain.ValidateLoanTerms(req.PrincipalCents, req.AnnualPercent, req.Periods, req.Type); err != nil {
		return Loan{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	rateMicro := AnnualPercentToRateMicro(req.AnnualPercent)

	// Build the schedule once to validate the rate/term combination produces
	// a well-formed plan before persisting anything.
	plan, err := amort.Build(amort.ScheduleInput{
		Outstanding:       req.PrincipalCents,
		PeriodicRateMicro: rateMicro,
		Periods:           req.Periods,
		Type:              req.Type,
	})
	if err != nil {
		return Loan{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := amort.ValidateSchedule(plan, req.PrincipalCents); err != nil {
		return Loan{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}

	var l Loan
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		found, err := s.borrowerExists(ctx, tx, req.BorrowerID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("%w: borrower not found", ErrNotFound)
		}
		seq, err := s.store.NextSeq(ctx, tx)
		if err != nil {
			return err
		}
		l = Loan{
			LoanID:            newID("loan"),
			BorrowerID:        req.BorrowerID,
			Principal:         req.PrincipalCents,
			AnnualPercent:     req.AnnualPercent,
			OriginalRateMicro: rateMicro,
			CurrentRateMicro:  rateMicro,
			CurrentPayment:    amort.PaymentAmount(req.PrincipalCents, rateMicro, req.Periods),
			OriginalN:         req.Periods,
			TermPeriods:       req.Periods,
			Type:              req.Type,
			Status:            StatusActive,
			CreatedSeq:        seq,
		}
		return s.store.InsertLoan(ctx, tx, &l)
	})
	if err != nil {
		return Loan{}, err
	}
	return l, nil
}

// GetLoan loads a loan by id.
func (s *Service) GetLoan(ctx context.Context, id string) (Loan, error) {
	var l Loan
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var found bool
		var err error
		l, found, err = s.store.GetLoan(ctx, tx, id)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		return nil
	})
	return l, err
}

// ListLoans returns loans matching the filter.
func (s *Service) ListLoans(ctx context.Context, f ListLoansFilter) ([]Loan, error) {
	var out []Loan
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = s.store.ListLoans(ctx, tx, f)
		return err
	})
	return out, err
}

// CancelLoan cancels an active loan that has no recorded payments.
func (s *Service) CancelLoan(ctx context.Context, id string) (Loan, error) {
	var l Loan
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		loan, found, err := s.store.GetLoan(ctx, tx, id)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		l = loan
		if IsTerminal(l.Status) {
			return fmt.Errorf("%w: loan is %s", ErrTerminal, l.Status)
		}
		n, err := s.store.CountPayments(ctx, tx, id)
		if err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("%w: cannot cancel a loan with recorded payments", ErrConflict)
		}
		l.Status = StatusCanceled
		return s.store.UpdateLoan(ctx, tx, &l)
	})
	return l, err
}

// --- derived state (read paths) ---

// LoanState is the derived view of a loan: the persisted loan plus the
// ledger-recomputed outstanding principal, the count of paid scheduled
// periods, and the rebuilt remaining schedule. It is the basis for balance,
// schedule, payoff, summary and projection responses.
type LoanState struct {
	Loan        Loan
	Outstanding int64
	PaidPeriods int
	Payments    []Payment
	Remaining   []amort.Period
	NextPayment int64 // planned payment for the next unpaid period (0 if none)
	NextPeriod  int   // 1-based number of the next unpaid period
	FullyPaid   bool
}

// loadState recomputes the full derived state for a loan from the ledger. It
// is the restart-recovery path: outstanding is Principal − Σ(principal paid),
// never a cached field.
func (s *Service) loadState(ctx context.Context, tx *sql.Tx, id string) (LoanState, error) {
	l, found, err := s.store.GetLoan(ctx, tx, id)
	if err != nil {
		return LoanState{}, err
	}
	if !found {
		return LoanState{}, ErrNotFound
	}
	payments, err := s.store.ListPaymentsByLoan(ctx, tx, id)
	if err != nil {
		return LoanState{}, err
	}
	paid, err := s.store.CountScheduledPaid(ctx, tx, id)
	if err != nil {
		return LoanState{}, err
	}
	outstanding := outstandingFromLedger(l.Principal, payments, -1)
	remaining, err := remainingSchedule(l, outstanding, paid)
	if err != nil {
		return LoanState{}, err
	}
	st := LoanState{
		Loan:        l,
		Outstanding: outstanding,
		PaidPeriods: paid,
		Payments:    payments,
		Remaining:   remaining,
		NextPeriod:  amort.NextUnpaidPeriod(paid, l.TermPeriods),
	}
	if len(remaining) > 0 {
		st.NextPayment = remaining[0].Payment
	}
	if paid >= l.TermPeriods {
		st.FullyPaid = true
	}
	return st, nil
}

// Schedule returns the full schedule: paid periods reconstructed from the
// ledger, followed by the rebuilt remaining plan.
func (s *Service) Schedule(ctx context.Context, id string) (ScheduleResponse, error) {
	var resp ScheduleResponse
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		st, err := s.loadState(ctx, tx, id)
		if err != nil {
			return err
		}
		resp = buildScheduleResponse(st)
		return nil
	})
	return resp, err
}

// BalanceResponse answers as-of outstanding principal queries.
type BalanceResponse struct {
	LoanID        string `json:"loan_id"`
	AsOf          int    `json:"as_of"`
	Outstanding   int64  `json:"outstanding_cents"`
	Principal     int64  `json:"principal_cents"`
	PaidPrincipal int64  `json:"paid_principal_cents"`
}

// Balance returns the outstanding principal at as-of period `asOf`. If asOf < 0
// the current outstanding is returned. Computed from the ledger, not cached.
func (s *Service) Balance(ctx context.Context, id string, asOf int) (BalanceResponse, error) {
	var resp BalanceResponse
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		l, found, err := s.store.GetLoan(ctx, tx, id)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		payments, err := s.store.ListPaymentsByLoan(ctx, tx, id)
		if err != nil {
			return err
		}
		var asOfPtr *int
		if asOf >= 0 {
			queryAsOf := asOf - 1
			asOfPtr = &queryAsOf
		}
		paid, err := s.store.SumPrincipalPaid(ctx, tx, id, asOfPtr)
		if err != nil {
			return err
		}
		if asOf < 0 {
			asOf = l.TermPeriods
		}
		resp = BalanceResponse{
			LoanID:        id,
			AsOf:          asOf,
			Outstanding:   l.Principal - paid,
			Principal:     l.Principal,
			PaidPrincipal: paid,
		}
		_ = payments
		return nil
	})
	return resp, err
}

// PayoffResponse answers the "what do I owe to close this now" query.
type PayoffResponse struct {
	LoanID          string `json:"loan_id"`
	AsOf            int    `json:"as_of"`
	Outstanding     int64  `json:"outstanding_cents"`
	AccruedInterest int64  `json:"accrued_interest_cents"`
	Payoff          int64  `json:"payoff_cents"`
}

// Payoff returns the amount needed to fully close the loan at as-of period.
// Payoff = outstanding principal + interest scheduled through asOf but not yet
// paid. (No prepayment penalty.)
func (s *Service) Payoff(ctx context.Context, id string, asOf int) (PayoffResponse, error) {
	var resp PayoffResponse
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		st, err := s.loadState(ctx, tx, id)
		if err != nil {
			return err
		}
		if asOf < 0 {
			asOf = st.NextPeriod
		}
		fullSched, err := amort.Build(amort.ScheduleInput{
			Outstanding:       st.Loan.Principal,
			PeriodicRateMicro: st.Loan.CurrentRateMicro,
			Periods:           st.Loan.TermPeriods,
			Type:              st.Loan.Type,
		})
		if err != nil {
			return err
		}
		accrued := amort.AccruedInterest(fullSched, asOf)
		var interestPaid int64
		for i := range st.Payments {
			if st.Payments[i].Seq <= asOf {
				interestPaid += st.Payments[i].Interest
			}
		}
		resp = PayoffResponse{
			LoanID:          id,
			AsOf:            asOf,
			Outstanding:     st.Outstanding,
			AccruedInterest: accrued - interestPaid,
			Payoff:          st.Outstanding + (accrued - interestPaid),
		}
		return nil
	})
	return resp, err
}

// SummaryResponse aggregates a loan's lifetime and current numbers.
type SummaryResponse struct {
	LoanID        string     `json:"loan_id"`
	Principal     int64      `json:"principal_cents"`
	PaidPrincipal int64      `json:"paid_principal_cents"`
	PaidInterest  int64      `json:"paid_interest_cents"`
	TotalPaid     int64      `json:"total_paid_cents"`
	Outstanding   int64      `json:"outstanding_cents"`
	PaidPeriods   int        `json:"paid_periods"`
	TermPeriods   int        `json:"term_periods"`
	NextPeriod    int        `json:"next_period"`
	NextPayment   int64      `json:"next_payment_cents"`
	NextPrincipal int64      `json:"next_principal_cents"`
	NextInterest  int64      `json:"next_interest_cents"`
	FullyPaid     bool       `json:"fully_paid"`
	Status        LoanStatus `json:"status"`
}

// Summary returns the aggregate summary for a loan.
func (s *Service) Summary(ctx context.Context, id string) (SummaryResponse, error) {
	var resp SummaryResponse
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		st, err := s.loadState(ctx, tx, id)
		if err != nil {
			return err
		}
		var paidP, paidI int64
		for i := range st.Payments {
			paidP += st.Payments[i].Principal
			paidI += st.Payments[i].Interest
		}
		resp = SummaryResponse{
			LoanID:        id,
			Principal:     st.Loan.Principal,
			PaidPrincipal: paidP,
			PaidInterest:  paidI,
			TotalPaid:     paidP + paidI,
			Outstanding:   st.Outstanding,
			PaidPeriods:   st.PaidPeriods,
			TermPeriods:   st.Loan.TermPeriods,
			NextPeriod:    st.NextPeriod,
			NextPayment:   st.NextPayment,
			FullyPaid:     st.FullyPaid,
			Status:        st.Loan.Status,
		}
		if len(st.Remaining) > 0 {
			resp.NextPrincipal = st.Remaining[0].Principal
			resp.NextInterest = st.Remaining[0].Interest
		}
		return nil
	})
	return resp, err
}

// ProjectionResponse returns the remaining schedule preview, optionally
// limited to `periods` rows.
type ProjectionResponse struct {
	LoanID      string         `json:"loan_id"`
	Outstanding int64          `json:"outstanding_cents"`
	Remaining   int            `json:"remaining_periods"`
	Periods     []amort.Period `json:"periods"`
}

// Projection returns the rebuilt remaining schedule.
func (s *Service) Projection(ctx context.Context, id string, limit int) (ProjectionResponse, error) {
	var resp ProjectionResponse
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		st, err := s.loadState(ctx, tx, id)
		if err != nil {
			return err
		}
		resp = ProjectionResponse{
			LoanID:      id,
			Outstanding: st.Outstanding,
			Remaining:   len(st.Remaining),
			Periods:     st.Remaining,
		}
		if limit > 0 && limit < len(st.Remaining) {
			resp.Periods = st.Remaining[:limit]
		}
		return nil
	})
	return resp, err
}

// --- payment writes ---

// RecordPaymentRequest is the JSON body for POST /loans/{id}/payments.
type RecordPaymentRequest struct {
	AmountCents int64 `json:"amount_cents"`
}

// RecordPayment applies a scheduled payment to the next unpaid period. The
// amount must exactly equal the planned payment for that period (tail-corrected
// last period included); excess must go through Prepay. The payment and any
// loan closure happen in one transaction.
func (s *Service) RecordPayment(ctx context.Context, loanID string, req RecordPaymentRequest) (Payment, Loan, error) {
	if req.AmountCents <= 0 {
		return Payment{}, Loan{}, fmt.Errorf("%w: amount must be > 0", ErrInvalid)
	}
	var pay Payment
	var l Loan
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		st, err := s.loadState(ctx, tx, loanID)
		if err != nil {
			return err
		}
		l = st.Loan
		if IsTerminal(l.Status) {
			return fmt.Errorf("%w: loan is %s", ErrTerminal, l.Status)
		}
		if st.FullyPaid {
			return fmt.Errorf("%w: loan is already fully paid", ErrConflict)
		}
		if len(st.Remaining) == 0 {
			return fmt.Errorf("%w: no remaining periods", ErrConflict)
		}
		next := st.Remaining[0]
		if req.AmountCents != next.Payment {
			return fmt.Errorf("%w: expected %d for period %d, got %d", ErrAmountMismatch, next.Payment, next.Number, req.AmountCents)
		}
		seq, err := s.store.NextSeq(ctx, tx)
		if err != nil {
			return err
		}
		pay = Payment{
			PaymentID:  newID("pay"),
			LoanID:     loanID,
			Seq:        next.Number - 1,
			Amount:     req.AmountCents,
			Principal:  next.Principal,
			Interest:   next.Interest,
			Type:       PaymentScheduled,
			CreatedSeq: seq,
		}
		if err := s.store.InsertPayment(ctx, tx, &pay); err != nil {
			return err
		}
		// If this was the last planned period and the principal is now zero,
		// close the loan in the same transaction.
		newPaid := st.PaidPeriods + 1
		if newPaid >= l.TermPeriods || st.Outstanding-next.Principal <= 0 {
			l.Status = StatusClosed
			if err := s.store.UpdateLoan(ctx, tx, &l); err != nil {
				return err
			}
		}
		return nil
	})
	return pay, l, err
}

// PrepayRequest is the JSON body for POST /loans/{id}/prepayments.
type PrepayRequest struct {
	AmountCents int64          `json:"amount_cents"`
	Strategy    PrepayStrategy `json:"strategy"`
}

// Prepay applies an extra principal payment and recasts the remaining schedule.
// reduce_term keeps the payment and shortens the term; reduce_payment keeps the
// term and lowers the payment. The payment, the recast and the loan-state
// update commit in one transaction.
func (s *Service) Prepay(ctx context.Context, loanID string, req PrepayRequest) (Payment, Loan, error) {
	if req.AmountCents <= 0 {
		return Payment{}, Loan{}, fmt.Errorf("%w: amount must be > 0", ErrInvalid)
	}
	if !ValidPrepayStrategy(req.Strategy) {
		return Payment{}, Loan{}, fmt.Errorf("%w: unknown strategy %q", ErrInvalid, req.Strategy)
	}
	var pay Payment
	var l Loan
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		st, err := s.loadState(ctx, tx, loanID)
		if err != nil {
			return err
		}
		l = st.Loan
		if IsTerminal(l.Status) {
			return fmt.Errorf("%w: loan is %s", ErrTerminal, l.Status)
		}
		if st.FullyPaid {
			return fmt.Errorf("%w: loan is already fully paid", ErrConflict)
		}
		if req.AmountCents > st.Outstanding {
			return fmt.Errorf("%w: %d > outstanding %d", ErrPrepayTooLarge, req.AmountCents, st.Outstanding)
		}
		// reduce_term keeps a fixed per-period payment, which is only defined
		// for equal_installment loans; the other types have variable payments.
		if req.Strategy == ReduceTerm && l.Type != EqualInstallment {
			return fmt.Errorf("%w: reduce_term only applies to equal_installment loans", ErrInvalid)
		}
		// A prepayment applies at the current next-period boundary: its Seq is
		// the next unpaid period number so as-of balances include it correctly
		// from that period onward.
		seq := st.NextPeriod
		newOutstanding := st.Outstanding - req.AmountCents
		in := amort.RecastInput{
			Type:              l.Type,
			Outstanding:       newOutstanding,
			PeriodicRateMicro: l.CurrentRateMicro,
			PaidPeriods:       st.PaidPeriods,
			OriginalTerm:      l.TermPeriods,
			CurrentPayment:    l.CurrentPayment, // authoritative stored payment
		}
		var res amort.RecastResult
		if req.Strategy == ReduceTerm {
			res, err = amort.RecastReduceTerm(in)
		} else {
			res, err = amort.RecastReducePayment(in)
		}
		if err != nil {
			return err
		}
		// Stamp and persist the prepayment.
		dbSeq, err := s.store.NextSeq(ctx, tx)
		if err != nil {
			return err
		}
		pay = Payment{
			PaymentID:  newID("pay"),
			LoanID:     loanID,
			Seq:        seq,
			Amount:     req.AmountCents,
			Principal:  req.AmountCents,
			Interest:   0,
			Type:       PaymentPrepayment,
			Strategy:   req.Strategy,
			CreatedSeq: dbSeq,
		}
		if err := s.store.InsertPayment(ctx, tx, &pay); err != nil {
			return err
		}
		// Apply the recast to the loan row. reduce_payment recomputes the
		// stored per-period payment over the new outstanding and remaining
		// periods; reduce_term keeps CurrentPayment unchanged (it's kept fixed
		// and the term shortens).
		l.TermPeriods = res.TermPeriods
		if req.Strategy == ReducePayment && newOutstanding > 0 {
			remaining := res.TermPeriods
			if remaining > 0 {
				l.CurrentPayment = amort.PaymentAmount(newOutstanding, l.CurrentRateMicro, remaining)
			}
		}
		if newOutstanding <= 0 {
			l.CurrentPayment = 0
		}
		return s.store.UpdateLoan(ctx, tx, &l)
	})
	return pay, l, err
}

// ChangeRateRequest is the JSON body for POST /loans/{id}/rate-changes.
type ChangeRateRequest struct {
	AnnualPercent float64 `json:"annual_rate_percent"`
}

// ChangeRate (refinance) replaces the periodic rate for the remaining term and
// recomputes the payment. Past payments are immutable. The rate update commits
// in one transaction with no payment row (rate changes are not payments).
func (s *Service) ChangeRate(ctx context.Context, loanID string, req ChangeRateRequest) (Loan, error) {
	if req.AnnualPercent < 0 {
		return Loan{}, fmt.Errorf("%w: annual rate must be >= 0", ErrInvalid)
	}
	rateMicro := AnnualPercentToRateMicro(req.AnnualPercent)
	var l Loan
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		st, err := s.loadState(ctx, tx, loanID)
		if err != nil {
			return err
		}
		l = st.Loan
		if IsTerminal(l.Status) {
			return fmt.Errorf("%w: loan is %s", ErrTerminal, l.Status)
		}
		if st.FullyPaid {
			return fmt.Errorf("%w: loan is already fully paid", ErrConflict)
		}
		// Validate the new rate produces a well-formed remaining schedule and
		// recompute the per-period payment at the new rate.
		remaining := l.TermPeriods - st.PaidPeriods + 1
		if remaining > 0 && st.Outstanding > 0 {
			res, err := amort.RecastRateChange(amort.RecastInput{
				Type:              l.Type,
				Outstanding:       st.Outstanding,
				PeriodicRateMicro: rateMicro,
				PaidPeriods:       st.PaidPeriods,
				OriginalTerm:      l.TermPeriods,
			})
			if err != nil {
				return fmt.Errorf("%w: %v", ErrInvalid, err)
			}
			if len(res.Schedule) > 0 {
				l.CurrentPayment = res.Schedule[0].Payment
			}
		}
		l.CurrentRateMicro = rateMicro
		// Recompute display annual percent from the new rate for round-trip.
		l.AnnualPercent = RateMicroToAnnualPercent(rateMicro)
		return s.store.UpdateLoan(ctx, tx, &l)
	})
	return l, err
}

// --- payment reads ---

// ListPaymentsByLoan returns the payment ledger for a loan.
func (s *Service) ListPaymentsByLoan(ctx context.Context, loanID string) ([]Payment, error) {
	var out []Payment
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		_, found, err := s.store.GetLoan(ctx, tx, loanID)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		out, err = s.store.ListPaymentsByLoan(ctx, tx, loanID)
		return err
	})
	return out, err
}

// GetPayment loads a single payment.
func (s *Service) GetPayment(ctx context.Context, loanID, paymentID string) (Payment, error) {
	var p Payment
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		_, found, err := s.store.GetLoan(ctx, tx, loanID)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		var ok bool
		p, ok, err = s.store.GetPayment(ctx, tx, loanID, paymentID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		return nil
	})
	return p, err
}

// ListPayments returns payment rows matching the filter (global ledger).
func (s *Service) ListPayments(ctx context.Context, f ListPaymentsFilter) ([]Payment, error) {
	out := make([]Payment, 0)
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = s.store.ListPayments(ctx, tx, f)
		return err
	})
	return out, err
}

// --- stats / audit ---

// StatsResponse aggregates portfolio-wide numbers.
type StatsResponse struct {
	LoansByStatus     map[LoanStatus]int `json:"loans_by_status"`
	TotalLoans        int                `json:"total_loans"`
	ActiveOutstanding int64              `json:"active_outstanding_cents"`
	TotalInterestPaid int64              `json:"total_interest_paid_cents"`
	TotalBorrowers    int                `json:"total_borrowers"`
	TotalPayments     int                `json:"total_payments"`
}

// Stats computes portfolio aggregates.
func (s *Service) Stats(ctx context.Context) (StatsResponse, error) {
	var resp StatsResponse
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		resp.LoansByStatus, err = s.store.CountLoansByStatus(ctx, tx)
		if err != nil {
			return err
		}
		for _, c := range resp.LoansByStatus {
			resp.TotalLoans += c
		}
		resp.ActiveOutstanding, err = s.store.SumPrincipalOut(ctx, tx)
		if err != nil {
			return err
		}
		resp.TotalInterestPaid, err = s.store.SumInterestPaid(ctx, tx)
		if err != nil {
			return err
		}
		resp.TotalBorrowers, err = s.store.CountBorrowers(ctx, tx)
		if err != nil {
			return err
		}
		// Count all payments across all loans.
		all, err := s.store.ListPayments(ctx, tx, ListPaymentsFilter{Limit: 0})
		if err != nil {
			return err
		}
		resp.TotalPayments = len(all)
		return nil
	})
	return resp, err
}

// RecomputeReport is the result of a consistency recompute: for each active
// loan the ledger-derived outstanding principal is compared against the plan's
// remaining principal. A healthy portfolio has zero drift.
type RecomputeReport struct {
	Checked int              `json:"checked"`
	Drift   []RecomputeDrift `json:"drift"`
	OK      bool             `json:"ok"`
}

// RecomputeDrift records a single inconsistency.
type RecomputeDrift struct {
	LoanID            string `json:"loan_id"`
	LedgerOutstanding int64  `json:"ledger_outstanding_cents"`
	PlanOutstanding   int64  `json:"plan_outstanding_cents"`
}

// Recompute rebuilds every loan's state from the ledger and reports any drift
// between the ledger-derived outstanding and the schedule's expected remaining
// principal. This is the restart-recovery verification: a healthy store has no
// drift because outstanding is always a pure function of the ledger.
func (s *Service) Recompute(ctx context.Context) (RecomputeReport, error) {
	var rep RecomputeReport
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		loans, err := s.store.ListLoans(ctx, tx, ListLoansFilter{Limit: 0})
		if err != nil {
			return err
		}
		rep.OK = true
		for i := range loans {
			l := loans[i]
			rep.Checked++
			st, err := s.loadState(ctx, tx, l.LoanID)
			if err != nil {
				return err
			}
			// Plan outstanding = the principal the remaining schedule still
			// expects to collect. For a healthy loan this equals the ledger
			// outstanding. We compute it as the sum of remaining principal.
			planOut := amort.SumPrincipal(st.Remaining)
			if planOut != st.Outstanding {
				rep.Drift = append(rep.Drift, RecomputeDrift{
					LoanID:            l.LoanID,
					LedgerOutstanding: st.Outstanding,
					PlanOutstanding:   planOut,
				})
				rep.OK = false
			}
		}
		return nil
	})
	return rep, err
}

// --- helpers ---

// ScheduleResponse is the full schedule: paid periods (from ledger) + remaining
// plan, with a running balance.
type ScheduleResponse struct {
	LoanID         string         `json:"loan_id"`
	Periods        []amort.Period `json:"periods"`
	TotalPrincipal int64          `json:"total_principal_cents"`
	TotalInterest  int64          `json:"total_interest_cents"`
	TotalPayment   int64          `json:"total_payment_cents"`
}

// buildScheduleResponse reconstructs the full schedule: paid periods are
// rebuilt from the ledger (principal/interest actually paid), remaining periods
// from the rebuilt plan, with a continuous running balance.
func buildScheduleResponse(st LoanState) ScheduleResponse {
	resp := ScheduleResponse{LoanID: st.Loan.LoanID}
	balance := st.Loan.Principal
	// Paid periods: ordered by seq. A prepayment at seq k is folded into the
	// running balance but does not get its own schedule row; only scheduled
	// payments produce schedule periods (one per paid period).
	paidByPeriod := make(map[int]Payment)
	for i := range st.Payments {
		p := st.Payments[i]
		if p.Type == PaymentScheduled {
			paidByPeriod[p.Seq] = p
		} else {
			// Prepayment reduces the balance immediately; track its effect.
			balance -= p.Principal
		}
	}
	for n := 1; n <= st.PaidPeriods; n++ {
		p := paidByPeriod[n]
		balance -= p.Principal
		resp.Periods = append(resp.Periods, amort.Period{
			Number:    n,
			Principal: p.Principal,
			Interest:  p.Interest,
			Payment:   p.Amount,
			Balance:   balance,
		})
	}
	for i := range st.Remaining {
		balance -= st.Remaining[i].Principal
		row := st.Remaining[i]
		row.Balance = balance
		resp.Periods = append(resp.Periods, row)
	}
	resp.TotalPrincipal = amort.SumPrincipal(resp.Periods)
	resp.TotalInterest = amort.SumInterest(resp.Periods)
	resp.TotalPayment = amort.SumPayment(resp.Periods)
	return resp
}

// inTx runs fn inside a store transaction. On error the tx is rolled back and
// the error is returned; the helper unwraps sql.ErrNoRows where appropriate.
func (s *Service) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.store.BeginTx(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// borrowerExists is a small read helper.
func (s *Service) borrowerExists(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	_, found, err := s.store.GetBorrower(ctx, tx, id)
	return found, err
}

// newID returns a random hex identifier with the given prefix. crypto/rand
// keeps ids unique without a per-table counter (the single-writer tx would
// also make COUNT+1 safe, but random ids survive rollbacks cleanly).
func newID(prefix string) string {
	var b [8]byte
	if _, err := crand.Read(b[:]); err != nil {
		// crypto/rand should not fail in practice; fall back to a stable but
		// unique-enough id so the engine never blocks on id generation.
		return prefix + "-00000000"
	}
	return prefix + "-" + hex.EncodeToString(b[:])
}
