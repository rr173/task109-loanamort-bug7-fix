package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"task109-loanamort/internal/loan"

	_ "modernc.org/sqlite" // pure-Go SQLite driver; CGO_ENABLED=0 compatible
)

// Store wraps a *sql.DB connection to the loan database. It is safe for
// concurrent use: database/sql pools connections and modernc.org/sqlite
// serializes writers via its own mutex plus BEGIN IMMEDIATE in the callers.
type Store struct {
	db *sql.DB
}

// Open creates or opens the SQLite file at path, applies the schema and tunes
// pragmatic options for durability and concurrency:
//   - journal_mode=WAL: readers don't block a single writer;
//   - busy_timeout: a concurrent writer waits rather than failing fast;
//   - synchronous=NORMAL: WAL-safe without forcing an fsync per commit;
//   - _txlock=immediate: every BEGIN is BEGIN IMMEDIATE, serializing writers.
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_txlock=immediate",
		path,
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	// SQLite effectively serializes writes; one connection is enough and avoids
	// "database is locked" from interleaved write transactions on extra conns.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// BeginTx starts an IMMEDIATE transaction (see Open for _txlock=immediate).
// Callers must Commit or Rollback.
func (s *Store) BeginTx(ctx context.Context) (*sql.Tx, error) {
	return s.db.BeginTx(ctx, nil)
}

// NextSeq atomically returns and increments the global monotonic sequence
// counter from the meta table. The increment and the row inserts that consume
// it must run in the same transaction so the stamp reflects insertion order
// even under a later rollback.
//
// The two-step read-after-bump is safe because the IMMEDIATE transaction and
// the single pooled connection serialize writers: no other tx can interleave
// between the UPSERT and the SELECT.
func (s *Store) NextSeq(ctx context.Context, tx *sql.Tx) (int64, error) {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO meta(key,value) VALUES('next_seq',1)
		 ON CONFLICT(key) DO UPDATE SET value=value+1`,
	); err != nil {
		return 0, fmt.Errorf("next_seq bump: %w", err)
	}
	var seq int64
	if err := tx.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key='next_seq'`,
	).Scan(&seq); err != nil {
		return 0, fmt.Errorf("next_seq read: %w", err)
	}
	return seq - 1, nil // return the pre-increment value just consumed
}

// --- borrowers ---

// InsertBorrower persists a borrower row.
func (s *Store) InsertBorrower(ctx context.Context, tx *sql.Tx, b *loan.Borrower) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO borrowers(borrower_id, name, created_seq) VALUES(?,?,?)`,
		b.BorrowerID, b.Name, b.CreatedSeq,
	)
	return err
}

// GetBorrower loads a borrower by id. found is false when absent.
func (s *Store) GetBorrower(ctx context.Context, tx *sql.Tx, id string) (loan.Borrower, bool, error) {
	row := tx.QueryRowContext(ctx,
		`SELECT borrower_id, name, created_seq FROM borrowers WHERE borrower_id=?`, id,
	)
	var b loan.Borrower
	err := row.Scan(&b.BorrowerID, &b.Name, &b.CreatedSeq)
	if err == sql.ErrNoRows {
		return loan.Borrower{}, false, nil
	}
	if err != nil {
		return loan.Borrower{}, false, err
	}
	return b, true, nil
}

// ListBorrowers returns up to limit borrowers (limit<=0 means no limit).
func (s *Store) ListBorrowers(ctx context.Context, tx *sql.Tx, limit int) ([]loan.Borrower, error) {
	q := `SELECT borrower_id, name, created_seq FROM borrowers ORDER BY created_seq, borrower_id`
	args := []any{}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]loan.Borrower, 0)
	for rows.Next() {
		var b loan.Borrower
		if err := rows.Scan(&b.BorrowerID, &b.Name, &b.CreatedSeq); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// CountBorrowers returns the total borrower count.
func (s *Store) CountBorrowers(ctx context.Context, tx *sql.Tx) (int, error) {
	var c int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM borrowers`).Scan(&c)
	return c, err
}

// --- loans ---

// InsertLoan persists a loan row.
func (s *Store) InsertLoan(ctx context.Context, tx *sql.Tx, l *loan.Loan) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO loans(loan_id, borrower_id, principal, annual_percent, original_rate_micro,
		   current_rate_micro, current_payment, original_n, term_periods, type, status, created_seq)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		l.LoanID, l.BorrowerID, l.Principal, l.AnnualPercent, l.OriginalRateMicro,
		l.CurrentRateMicro, l.CurrentPayment, l.OriginalN, l.TermPeriods, l.Type, l.Status, l.CreatedSeq,
	)
	return err
}

// GetLoan loads a loan by id. found is false when absent.
func (s *Store) GetLoan(ctx context.Context, tx *sql.Tx, id string) (loan.Loan, bool, error) {
	row := tx.QueryRowContext(ctx,
		`SELECT loan_id, borrower_id, principal, annual_percent, original_rate_micro,
		   current_rate_micro, current_payment, original_n, term_periods, type, status, created_seq
		 FROM loans WHERE loan_id=?`, id,
	)
	var l loan.Loan
	err := scanLoan(row, &l)
	if err == sql.ErrNoRows {
		return loan.Loan{}, false, nil
	}
	if err != nil {
		return loan.Loan{}, false, err
	}
	return l, true, nil
}

// ListLoans returns loans matching the filter. Empty filter fields are ignored.
func (s *Store) ListLoans(ctx context.Context, tx *sql.Tx, f loan.ListLoansFilter) ([]loan.Loan, error) {
	var (
		clauses []string
		args    []any
	)
	if f.BorrowerID != "" {
		clauses = append(clauses, "borrower_id=?")
		args = append(args, f.BorrowerID)
	}
	if f.Status != "" {
		clauses = append(clauses, "status=?")
		args = append(args, f.Status)
	}
	if f.Type != "" {
		clauses = append(clauses, "type=?")
		args = append(args, f.Type)
	}
	q := `SELECT loan_id, borrower_id, principal, annual_percent, original_rate_micro,
	        current_rate_micro, current_payment, original_n, term_periods, type, status, created_seq
	      FROM loans`
	if len(clauses) > 0 {
		q += " WHERE " + strings.Join(clauses, " AND ")
	}
	q += " ORDER BY created_seq, loan_id"
	if f.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, f.Limit)
	}
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]loan.Loan, 0)
	for rows.Next() {
		var l loan.Loan
		if err := scanLoan(rows, &l); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// UpdateLoan persists mutable loan fields (current_rate_micro,
// current_payment, term_periods, status). Principal and original fields are
// immutable.
func (s *Store) UpdateLoan(ctx context.Context, tx *sql.Tx, l *loan.Loan) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE loans SET current_rate_micro=?, current_payment=?, term_periods=?, status=? WHERE loan_id=?`,
		l.CurrentRateMicro, l.CurrentPayment, l.TermPeriods, l.Status, l.LoanID,
	)
	return err
}

// CountLoansByStatus returns the count of loans in each status bucket.
func (s *Store) CountLoansByStatus(ctx context.Context, tx *sql.Tx) (map[loan.LoanStatus]int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT status, COUNT(*) FROM loans GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[loan.LoanStatus]int)
	for rows.Next() {
		var st string
		var c int
		if err := rows.Scan(&st, &c); err != nil {
			return nil, err
		}
		out[loan.LoanStatus(st)] = c
	}
	return out, rows.Err()
}

// SumPrincipalOut returns the total outstanding principal across active loans.
// Computed as SUM(active principal) − SUM(principal paid on active loans).
// Every column is table-qualified because both loans and payments have a
// principal column, which would otherwise be ambiguous.
func (s *Store) SumPrincipalOut(ctx context.Context, tx *sql.Tx) (int64, error) {
	var out sql.NullInt64
	err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(l.principal),0) - COALESCE((
		     SELECT SUM(p.principal) FROM payments p
		     JOIN loans l2 ON p.loan_id=l2.loan_id WHERE l2.status='active'),0)
		 FROM loans l WHERE l.status='active'`,
	).Scan(&out)
	if err != nil {
		return 0, err
	}
	return out.Int64, nil
}

// SumInterestPaid returns the total interest paid across all loans.
func (s *Store) SumInterestPaid(ctx context.Context, tx *sql.Tx) (int64, error) {
	var out sql.NullInt64
	err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(interest),0) FROM payments WHERE type='scheduled'`,
	).Scan(&out)
	if err != nil {
		return 0, err
	}
	return out.Int64, nil
}

// --- payments ---

// InsertPayment persists a payment-ledger row.
func (s *Store) InsertPayment(ctx context.Context, tx *sql.Tx, p *loan.Payment) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO payments(payment_id, loan_id, seq, amount, principal, interest, type, strategy, created_seq)
		 VALUES(?,?,?,?,?,?,?,?,?)`,
		p.PaymentID, p.LoanID, p.Seq, p.Amount, p.Principal, p.Interest, p.Type, p.Strategy, p.CreatedSeq,
	)
	return err
}

// GetPayment loads a single payment by id within a loan.
func (s *Store) GetPayment(ctx context.Context, tx *sql.Tx, loanID, paymentID string) (loan.Payment, bool, error) {
	row := tx.QueryRowContext(ctx,
		`SELECT payment_id, loan_id, seq, amount, principal, interest, type, strategy, created_seq
		 FROM payments WHERE loan_id=? AND payment_id=?`, loanID, paymentID,
	)
	var p loan.Payment
	err := scanPayment(row, &p)
	if err == sql.ErrNoRows {
		return loan.Payment{}, false, nil
	}
	if err != nil {
		return loan.Payment{}, false, err
	}
	return p, true, nil
}

// ListPaymentsByLoan returns the payment ledger for a loan, ordered by seq.
func (s *Store) ListPaymentsByLoan(ctx context.Context, tx *sql.Tx, loanID string) ([]loan.Payment, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT payment_id, loan_id, seq, amount, principal, interest, type, strategy, created_seq
		FROM payments WHERE loan_id=? ORDER BY seq, created_seq`, loanID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectPayments(rows)
}

// ListPayments returns payment rows matching the filter.
func (s *Store) ListPayments(ctx context.Context, tx *sql.Tx, f loan.ListPaymentsFilter) ([]loan.Payment, error) {
	var (
		clauses []string
		args    []any
	)
	if f.LoanID != "" {
		clauses = append(clauses, "payments.loan_id=?")
		args = append(args, f.LoanID)
	}
	if f.BorrowerID != "" {
		clauses = append(clauses, "loans.borrower_id=?")
		args = append(args, f.BorrowerID)
	}
	q := `SELECT payments.payment_id, payments.loan_id, payments.seq, payments.amount,
	        payments.principal, payments.interest, payments.type, payments.strategy, payments.created_seq
	      FROM payments JOIN loans ON payments.loan_id = loans.loan_id`
	if len(clauses) > 0 {
		q += " WHERE " + strings.Join(clauses, " AND ")
	}
	q += " ORDER BY payments.created_seq, payments.payment_id"
	if f.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, f.Limit)
	}
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectPayments(rows)
}

// SumPrincipalPaid returns the total principal paid for a loan. If asOfSeq is
// non-nil only payments with seq <= *asOfSeq are summed (the as-of balance
// formula).
func (s *Store) SumPrincipalPaid(ctx context.Context, tx *sql.Tx, loanID string, asOfSeq *int) (int64, error) {
	q := `SELECT COALESCE(SUM(principal),0) FROM payments WHERE loan_id=?`
	args := []any{loanID}
	if asOfSeq != nil {
		q += ` AND seq < ?`
		args = append(args, *asOfSeq)
	}
	var out sql.NullInt64
	err := tx.QueryRowContext(ctx, q, args...).Scan(&out)
	if err != nil {
		return 0, err
	}
	return out.Int64, nil
}

// CountScheduledPaid returns how many scheduled payments the loan has recorded.
func (s *Store) CountScheduledPaid(ctx context.Context, tx *sql.Tx, loanID string) (int, error) {
	var c int
	err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM payments WHERE loan_id=? AND type='scheduled'`, loanID,
	).Scan(&c)
	return c, err
}

// CountPayments returns the total payment rows for a loan.
func (s *Store) CountPayments(ctx context.Context, tx *sql.Tx, loanID string) (int, error) {
	var c int
	err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM payments WHERE loan_id=?`, loanID,
	).Scan(&c)
	return c, err
}

// --- helpers ---

// scanner abstracts *sql.Row and *sql.Rows so scanLoan/scanPayment serve both.
type scanner interface {
	Scan(dest ...any) error
}

func scanLoan(sc scanner, l *loan.Loan) error {
	return sc.Scan(
		&l.LoanID, &l.BorrowerID, &l.Principal, &l.AnnualPercent, &l.OriginalRateMicro,
		&l.CurrentRateMicro, &l.CurrentPayment, &l.OriginalN, &l.TermPeriods, &l.Type, &l.Status, &l.CreatedSeq,
	)
}

func scanPayment(sc scanner, p *loan.Payment) error {
	return sc.Scan(
		&p.PaymentID, &p.LoanID, &p.Seq, &p.Amount, &p.Principal, &p.Interest,
		&p.Type, &p.Strategy, &p.CreatedSeq,
	)
}

func collectPayments(rows *sql.Rows) ([]loan.Payment, error) {
	var out []loan.Payment
	for rows.Next() {
		var p loan.Payment
		if err := scanPayment(rows, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
