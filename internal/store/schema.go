// Package store is the SQLite persistence layer for the loan engine. It owns
// the schema and the low-level transaction primitives; the loan service
// layers business rules (schedule build, prepayment recast, rate change,
// as-of balance) on top.
//
// All write operations run inside a caller-begun transaction; Open uses
// _txlock=immediate so every BEGIN is BEGIN IMMEDIATE, serializing writers.
// A single pooled connection (SetMaxOpenConns(1)) avoids "database is
// locked" from interleaved write transactions on extra connections.
package store

// schema is applied on every Open; SQLite's IF NOT EXISTS makes it idempotent
// so a fresh file, a reused file and a post-crash file all converge to the
// same shape.
const schema = `
CREATE TABLE IF NOT EXISTS borrowers (
	borrower_id  TEXT PRIMARY KEY,
	name         TEXT NOT NULL,
	created_seq  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS loans (
	loan_id             TEXT PRIMARY KEY,
	borrower_id         TEXT NOT NULL,
	principal           INTEGER NOT NULL,
	annual_percent       REAL NOT NULL,
	original_rate_micro INTEGER NOT NULL,
	current_rate_micro  INTEGER NOT NULL,
	current_payment     INTEGER NOT NULL,
	original_n          INTEGER NOT NULL,
	term_periods         INTEGER NOT NULL,
	type                TEXT NOT NULL,
	status              TEXT NOT NULL,
	created_seq         INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_loans_borrower ON loans(borrower_id);
CREATE INDEX IF NOT EXISTS idx_loans_status   ON loans(status);
CREATE INDEX IF NOT EXISTS idx_loans_type     ON loans(type);

CREATE TABLE IF NOT EXISTS payments (
	payment_id  TEXT PRIMARY KEY,
	loan_id     TEXT NOT NULL,
	seq         INTEGER NOT NULL,
	amount      INTEGER NOT NULL,
	principal   INTEGER NOT NULL,
	interest    INTEGER NOT NULL,
	type        TEXT NOT NULL,
	strategy    TEXT NOT NULL DEFAULT '',
	created_seq INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_payments_loan ON payments(loan_id);
CREATE INDEX IF NOT EXISTS idx_payments_seq  ON payments(loan_id, seq);
CREATE INDEX IF NOT EXISTS idx_payments_type ON payments(loan_id, type);

-- meta holds a single monotonic sequence counter used to stamp created_seq on
-- every borrower/loan/payment row. It is the engine's logical clock: ordering
-- lists by created_seq reflects true insertion order across restarts. The row
-- is seeded on first Open.
CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value INTEGER NOT NULL
);
INSERT INTO meta(key, value) VALUES('next_seq', 1)
ON CONFLICT(key) DO NOTHING;
`
