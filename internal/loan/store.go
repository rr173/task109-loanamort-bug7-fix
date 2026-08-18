package loan

import (
	"context"
	"database/sql"
)

// Borrower is a loan recipient. Stored in the borrowers table.
type Borrower struct {
	BorrowerID string `json:"borrower_id"`
	Name       string `json:"name"`
	CreatedSeq int64  `json:"created_seq"`
}

// Loan is the persistent representation of an amortizing loan. Money fields
// are integer cents; CurrentRateMicro is the periodic (monthly) rate scaled by
// RateMicroScale.
//
// TermPeriods is mutable: a reduce_term prepayment shortens it. CurrentRateMicro
// is mutable: a rate-change (refinance) replaces it. The per-period payment and
// the remaining schedule are DERIVED (never stored): given Principal,
// CurrentRateMicro, TermPeriods, Type and the count of payments already made,
// the schedule is a pure function of these plus the ledger-derived outstanding
// principal. This is what makes restart recovery exact: nothing cached.
type Loan struct {
	LoanID            string     `json:"loan_id"`
	BorrowerID        string     `json:"borrower_id"`
	Principal         int64      `json:"principal"`             // cents, immutable
	AnnualPercent     float64    `json:"annual_rate_percent"`   // original, for display
	OriginalRateMicro int64      `json:"original_rate_micro"`   // periodic, original
	CurrentRateMicro  int64      `json:"current_rate_micro"`    // periodic, mutable (rate-change)
	OriginalN         int        `json:"original_n"`            // original term
	TermPeriods       int        `json:"term_periods"`          // current term (reduce_term shortens)
	CurrentPayment    int64      `json:"current_payment_cents"` // fixed per-period payment (equal_installment)
	Type              LoanType   `json:"type"`
	Status            LoanStatus `json:"status"`
	CreatedSeq        int64      `json:"created_seq"`
}

// Payment is one row of the append-only payment ledger. Every payment —
// scheduled or prepayment — carries the principal/interest split so the
// outstanding balance can be recomputed as Principal − Σ(Payment.Principal).
//
// Seq is the period number at which the payment is applied: a scheduled
// payment for period k has Seq=k; a prepayment made after period k has
// Seq=k. Outstanding(as_of=N) = Principal − Σ(Payment.Principal where
// Seq ≤ N), which is the restart-recovery formula.
type Payment struct {
	PaymentID  string         `json:"payment_id"`
	LoanID     string         `json:"loan_id"`
	Seq        int            `json:"seq"`       // period applied at (1-based)
	Amount     int64          `json:"amount"`    // total paid, cents
	Principal  int64          `json:"principal"` // principal portion, cents
	Interest   int64          `json:"interest"`  // interest portion, cents
	Type       PaymentType    `json:"type"`
	Strategy   PrepayStrategy `json:"strategy,omitempty"` // only for prepayments
	CreatedSeq int64          `json:"created_seq"`
}

// ListLoansFilter selects loans by the given optional fields; empty fields are
// ignored.
type ListLoansFilter struct {
	BorrowerID string
	Status     LoanStatus
	Type       LoanType
	Limit      int
}

// ListPaymentsFilter selects payment-ledger rows by optional fields.
type ListPaymentsFilter struct {
	LoanID     string
	BorrowerID string
	Limit      int
}

// Store is the persistence contract the service depends on. Every method runs
// inside a caller-supplied transaction (except BeginTx/Close) so that recording
// a payment and updating the loan row commit atomically. The concrete
// implementation lives in internal/store.
type Store interface {
	// Close releases the database handle.
	Close() error
	// BeginTx starts an IMMEDIATE transaction. Callers Commit or Rollback.
	BeginTx(ctx context.Context) (*sql.Tx, error)
	// NextSeq atomically returns and increments the global monotonic sequence
	// counter. It is the engine's logical clock: every borrower/loan/payment
	// row is stamped with it so lists reflect true insertion order across
	// restarts. Must be called inside the caller's transaction.
	NextSeq(ctx context.Context, tx *sql.Tx) (int64, error)

	// --- borrowers ---
	InsertBorrower(ctx context.Context, tx *sql.Tx, b *Borrower) error
	GetBorrower(ctx context.Context, tx *sql.Tx, id string) (Borrower, bool, error)
	ListBorrowers(ctx context.Context, tx *sql.Tx, limit int) ([]Borrower, error)
	CountBorrowers(ctx context.Context, tx *sql.Tx) (int, error)

	// --- loans ---
	InsertLoan(ctx context.Context, tx *sql.Tx, l *Loan) error
	GetLoan(ctx context.Context, tx *sql.Tx, id string) (Loan, bool, error)
	ListLoans(ctx context.Context, tx *sql.Tx, f ListLoansFilter) ([]Loan, error)
	UpdateLoan(ctx context.Context, tx *sql.Tx, l *Loan) error
	CountLoansByStatus(ctx context.Context, tx *sql.Tx) (map[LoanStatus]int, error)
	SumPrincipalOut(ctx context.Context, tx *sql.Tx) (int64, error)
	SumInterestPaid(ctx context.Context, tx *sql.Tx) (int64, error)

	// --- payments ---
	InsertPayment(ctx context.Context, tx *sql.Tx, p *Payment) error
	GetPayment(ctx context.Context, tx *sql.Tx, loanID, paymentID string) (Payment, bool, error)
	ListPaymentsByLoan(ctx context.Context, tx *sql.Tx, loanID string) ([]Payment, error)
	ListPayments(ctx context.Context, tx *sql.Tx, f ListPaymentsFilter) ([]Payment, error)
	// SumPrincipalPaid returns the total principal paid for a loan. If asOfSeq
	// is non-nil, only payments with seq <= *asOfSeq are summed.
	SumPrincipalPaid(ctx context.Context, tx *sql.Tx, loanID string, asOfSeq *int) (int64, error)
	// CountScheduledPaid returns how many scheduled payments the loan has.
	CountScheduledPaid(ctx context.Context, tx *sql.Tx, loanID string) (int, error)
	// CountPayments returns the total payment rows for a loan.
	CountPayments(ctx context.Context, tx *sql.Tx, loanID string) (int, error)
}
