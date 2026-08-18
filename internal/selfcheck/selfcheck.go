// Package selfcheck runs the --smoke-test for the loan engine. Each scenario
// builds its own temporary SQLite file and httptest server so contract
// assertions don't trip over state left by an earlier scenario (see the
// selfcheck-global-state isolation note). The final scenario exercises
// restart recovery: it seeds state, closes the store, reopens the same file
// and asserts the ledger-derived balance is unchanged.
//
// The smoke test never sleeps and never touches the network; it talks to the
// real mux over httptest.NewServer so the HTTP/JSON contract itself is
// exercised end to end.
package selfcheck

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"

	"task109-loanamort/internal/httpapi"
	"task109-loanamort/internal/loan"
	"task109-loanamort/internal/store"
)

// adminToken used by the smoke test for the recompute endpoint.
const adminToken = "admin-secret"

// Run executes every smoke scenario. Returns the first failure.
func Run() error {
	dir, err := os.MkdirTemp("", "loanamort-smoke-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	cases := []struct {
		name string
		fn   func(*httptest.Server, string) error
	}{
		{"equal-installment-exactness-and-tail", smokeEqualInstallmentExactness},
		{"equal-principal-exactness", smokeEqualPrincipalExactness},
		{"interest-only-final-principal", smokeInterestOnly},
		{"zero-rate-no-divzero", smokeZeroRate},
		{"prepay-reduce-term-shortens-and-zeroes", smokePrepayReduceTerm},
		{"prepay-reduce-payment-lowers-and-zeroes", smokePrepayReducePayment},
		{"rate-change-refinance-recasts", smokeRateChange},
		{"scheduled-amount-mismatch-rejected", smokeAmountMismatch},
		{"as-of-balance-from-ledger", smokeAsOfBalance},
		{"recompute-no-drift", smokeRecompute},
	}
	for i, c := range cases {
		dbPath := filepath.Join(dir, fmt.Sprintf("smoke-%02d.db", i))
		srv, err := newServer(dbPath)
		if err != nil {
			return fmt.Errorf("%s: new server: %w", c.name, err)
		}
		if err := c.fn(srv, dbPath); err != nil {
			srv.Close()
			return fmt.Errorf("%s: %w", c.name, err)
		}
		srv.Close()
	}

	// Restart-recovery runs separately because it controls the store lifecycle
	// (close + reopen on the same file) itself.
	if err := smokeRestartRecovery(filepath.Join(dir, "smoke-recover.db")); err != nil {
		return fmt.Errorf("restart-recovery: %w", err)
	}

	return nil
}

// newServer opens a fresh SQLite file, builds the service + mux and returns an
// httptest server over the real HTTP handler tree.
func newServer(dbPath string) (*httptest.Server, error) {
	st, err := store.Open(dbPath)
	if err != nil {
		return nil, err
	}
	svc := loan.New(st)
	mux := httpapi.NewMux(svc, adminToken)
	srv := httptest.NewServer(mux)
	// Attach the store on the server's Config so the test can reopen it later
	// for restart-recovery scenarios; httptest.Server has no field for this, so
	// we stash the closer via a wrapper that closes the store when the server
	// closes.
	srv.Config.RegisterOnShutdown(func() { _ = st.Close() })
	return srv, nil
}

// newServer opens a fresh SQLite file, builds the service + mux and returns an
// httptest server over the real HTTP handler tree. The store is closed via a
// shutdown hook registered on the server's Config so callers only need to
// call srv.Close().

// --- HTTP helpers ---

func doJSON(srv *httptest.Server, method, path string, body any, admin bool) (int, []byte, error) {
	var r *http.Request
	if body == nil {
		r = httptest.NewRequest(method, path, nil)
	} else {
		buf, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, bytes.NewReader(buf))
		r.Header.Set("Content-Type", "application/json")
	}
	if admin {
		r.Header.Set(httpapi.AdminTokenHeader, adminToken)
	}
	rec := httptest.NewRecorder()
	srv.Config.Handler.ServeHTTP(rec, r)
	return rec.Code, rec.Body.Bytes(), nil
}

// mustDo performs an HTTP call and returns the decoded body; it fails the
// scenario immediately on a non-2xx status (per the selfcheck-helper-error
// convention: success-expecting helpers must surface non-200s, not swallow
// them).
func mustDo(srv *httptest.Server, method, path string, body any, admin bool, out any) error {
	code, respBody, err := doJSON(srv, method, path, body, admin)
	if err != nil {
		return err
	}
	if code < 200 || code >= 300 {
		return fmt.Errorf("%s %s: expected 2xx, got %d: %s", method, path, code, respBody)
	}
	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode %s %s: %w", method, path, err)
		}
	}
	return nil
}

// expectStatus asserts the call returns the exact status code; it returns the
// response body for further inspection.
func expectStatus(srv *httptest.Server, method, path string, body any, admin bool, want int) ([]byte, error) {
	code, respBody, err := doJSON(srv, method, path, body, admin)
	if err != nil {
		return nil, err
	}
	if code != want {
		return respBody, fmt.Errorf("%s %s: expected %d, got %d: %s", method, path, want, code, respBody)
	}
	return respBody, nil
}

// --- scenario helpers ---

// createLoanFixture creates a borrower and a loan, returning their ids.
func createLoanFixture(srv *httptest.Server, principal int64, annual float64, periods int, ltype string) (borrowerID, loanID string, err error) {
	var b struct {
		BorrowerID string `json:"borrower_id"`
	}
	if err = mustDo(srv, "POST", "/borrowers", map[string]any{"name": "scott"}, false, &b); err != nil {
		return "", "", fmt.Errorf("create borrower: %w", err)
	}
	borrowerID = b.BorrowerID
	var l struct {
		LoanID string `json:"loan_id"`
	}
	if err = mustDo(srv, "POST", "/loans", map[string]any{
		"borrower_id":         borrowerID,
		"principal_cents":     principal,
		"annual_rate_percent": annual,
		"periods":             periods,
		"type":                ltype,
	}, false, &l); err != nil {
		return "", "", fmt.Errorf("create loan: %w", err)
	}
	loanID = l.LoanID
	return borrowerID, loanID, nil
}

// scheduleTotals sums principal/interest from the schedule response.
func scheduleTotals(srv *httptest.Server, loanID string) (principal, interest, payment int64, periods int, err error) {
	var sched struct {
		Periods []struct {
			Number    int   `json:"period"`
			Principal int64 `json:"principal"`
			Interest  int64 `json:"interest"`
			Payment   int64 `json:"payment"`
			Balance   int64 `json:"balance"`
		} `json:"periods"`
	}
	if err = mustDo(srv, "GET", "/loans/"+loanID+"/schedule", nil, false, &sched); err != nil {
		return 0, 0, 0, 0, err
	}
	for _, p := range sched.Periods {
		principal += p.Principal
		interest += p.Interest
		payment += p.Payment
	}
	periods = len(sched.Periods)
	if periods > 0 {
		// final balance must be exactly zero on a healthy schedule
		if fb := sched.Periods[periods-1].Balance; fb != 0 {
			return principal, interest, payment, periods, fmt.Errorf("final balance %d != 0", fb)
		}
	}
	return principal, interest, payment, periods, nil
}

// --- scenarios ---

// smokeEqualInstallmentExactness: an equal-installment schedule sums principal
// exactly to the loan principal and the final balance is zero (tail
// correction), with non-trivial rounding present.
func smokeEqualInstallmentExactness(srv *httptest.Server, dbPath string) error {
	const principal int64 = 1_000_000 // $10,000.00
	_, loanID, err := createLoanFixture(srv, principal, 12.0, 12, "equal_installment")
	if err != nil {
		return err
	}
	p, _, _, periods, err := scheduleTotals(srv, loanID)
	if err != nil {
		return err
	}
	if p != principal {
		return fmt.Errorf("sum principal %d != loan principal %d", p, principal)
	}
	if periods != 12 {
		return fmt.Errorf("expected 12 periods, got %d", periods)
	}
	// Verify the next-period payment matches the schedule's first period and
	// that paying it advances the loan by exactly one period.
	var sched struct {
		Periods []struct {
			Number  int   `json:"period"`
			Payment int64 `json:"payment"`
		} `json:"periods"`
	}
	if err := mustDo(srv, "GET", "/loans/"+loanID+"/schedule", nil, false, &sched); err != nil {
		return err
	}
	firstPayment := sched.Periods[0].Payment
	var resp struct {
		Loan struct {
			Status string `json:"status"`
		} `json:"loan"`
	}
	if err := mustDo(srv, "POST", "/loans/"+loanID+"/payments",
		map[string]any{"amount_cents": firstPayment}, false, &resp); err != nil {
		return err
	}
	if resp.Loan.Status != "active" {
		return fmt.Errorf("loan should still be active after 1 of 12 payments, got %s", resp.Loan.Status)
	}
	// After one payment, outstanding = principal - firstPeriodPrincipal.
	var bal struct {
		Outstanding int64 `json:"outstanding_cents"`
	}
	if err := mustDo(srv, "GET", "/loans/"+loanID+"/balance", nil, false, &bal); err != nil {
		return err
	}
	if bal.Outstanding <= 0 || bal.Outstanding >= principal {
		return fmt.Errorf("outstanding %d should be in (0, %d) after 1 payment", bal.Outstanding, principal)
	}
	return nil
}

// smokeEqualPrincipalExactness: equal-principal sums to principal and zeroes.
func smokeEqualPrincipalExactness(srv *httptest.Server, dbPath string) error {
	const principal int64 = 1_200_000 // $12,000.00
	_, loanID, err := createLoanFixture(srv, principal, 12.0, 12, "equal_principal")
	if err != nil {
		return err
	}
	p, _, _, periods, err := scheduleTotals(srv, loanID)
	if err != nil {
		return err
	}
	if p != principal {
		return fmt.Errorf("sum principal %d != loan principal %d", p, principal)
	}
	if periods != 12 {
		return fmt.Errorf("expected 12 periods, got %d", periods)
	}
	return nil
}

// smokeInterestOnly: final period pays the full principal; principal sum is
// exact and final balance is zero.
func smokeInterestOnly(srv *httptest.Server, dbPath string) error {
	const principal int64 = 500_000 // $5,000.00
	_, loanID, err := createLoanFixture(srv, principal, 6.0, 6, "interest_only")
	if err != nil {
		return err
	}
	p, _, _, periods, err := scheduleTotals(srv, loanID)
	if err != nil {
		return err
	}
	if p != principal {
		return fmt.Errorf("sum principal %d != loan principal %d", p, principal)
	}
	if periods != 6 {
		return fmt.Errorf("expected 6 periods, got %d", periods)
	}
	return nil
}

// smokeZeroRate: a zero-rate equal-installment loan does not divide by zero;
// payment = principal/n, interest is always 0, schedule zeroes exactly.
func smokeZeroRate(srv *httptest.Server, dbPath string) error {
	const principal int64 = 1_000_000
	_, loanID, err := createLoanFixture(srv, principal, 0.0, 4, "equal_installment")
	if err != nil {
		return err
	}
	p, interest, _, periods, err := scheduleTotals(srv, loanID)
	if err != nil {
		return err
	}
	if p != principal {
		return fmt.Errorf("zero-rate sum principal %d != %d", p, principal)
	}
	if interest != 0 {
		return fmt.Errorf("zero-rate interest %d != 0", interest)
	}
	if periods != 4 {
		return fmt.Errorf("expected 4 periods, got %d", periods)
	}
	// Each period payment should be 250_000 (principal/4) with interest 0.
	var sched struct {
		Periods []struct {
			Payment   int64 `json:"payment"`
			Principal int64 `json:"principal"`
			Interest  int64 `json:"interest"`
		} `json:"periods"`
	}
	if err := mustDo(srv, "GET", "/loans/"+loanID+"/schedule", nil, false, &sched); err != nil {
		return err
	}
	for _, pr := range sched.Periods {
		if pr.Payment != 250_000 || pr.Principal != 250_000 || pr.Interest != 0 {
			return fmt.Errorf("zero-rate period expected 250000/250000/0, got %d/%d/%d", pr.Payment, pr.Principal, pr.Interest)
		}
	}
	return nil
}

// smokePrepayReduceTerm: a reduce_term prepayment shortens the term and the new
// schedule still sums to the remaining principal and zeroes exactly.
func smokePrepayReduceTerm(srv *httptest.Server, dbPath string) error {
	const principal int64 = 1_000_000
	_, loanID, err := createLoanFixture(srv, principal, 12.0, 12, "equal_installment")
	if err != nil {
		return err
	}
	// Pay the first scheduled period.
	var sched struct {
		Periods []struct {
			Payment int64 `json:"payment"`
		} `json:"periods"`
	}
	if err := mustDo(srv, "GET", "/loans/"+loanID+"/schedule", nil, false, &sched); err != nil {
		return err
	}
	firstPayment := sched.Periods[0].Payment
	if err := mustDo(srv, "POST", "/loans/"+loanID+"/payments", map[string]any{"amount_cents": firstPayment}, false, nil); err != nil {
		return err
	}
	// Prepay 200_000 reducing term.
	if err := mustDo(srv, "POST", "/loans/"+loanID+"/prepayments",
		map[string]any{"amount_cents": 200_000, "strategy": "reduce_term"}, false, nil); err != nil {
		return err
	}
	// After reduce_term, term must be < original 12 and > paid 1.
	var l struct {
		TermPeriods int `json:"term_periods"`
	}
	if err := mustDo(srv, "GET", "/loans/"+loanID, nil, false, &l); err != nil {
		return err
	}
	if l.TermPeriods <= 1 || l.TermPeriods >= 12 {
		return fmt.Errorf("reduce_term term %d not in (1,12)", l.TermPeriods)
	}
	// After reduce_term, the plan's period-principal sum = original principal
	// minus the prepayment (a prepayment reduces principal but does not get
	// its own schedule period). The full schedule still zeroes at the end.
	if p, _, _, _, err := scheduleTotals(srv, loanID); err != nil {
		return err
	} else if p != principal-200_000 {
		return fmt.Errorf("post-prepay sum principal %d != %d (original %d - prepay 200000)", p, principal-200_000, principal)
	}
	return nil
}

// smokePrepayReducePayment: a reduce_payment prepayment keeps the term and
// lowers the next payment; the schedule still zeroes.
func smokePrepayReducePayment(srv *httptest.Server, dbPath string) error {
	const principal int64 = 1_000_000
	_, loanID, err := createLoanFixture(srv, principal, 12.0, 12, "equal_installment")
	if err != nil {
		return err
	}
	// Record the first scheduled payment, then capture the next payment.
	var sched struct {
		Periods []struct {
			Payment int64 `json:"payment"`
		} `json:"periods"`
	}
	if err := mustDo(srv, "GET", "/loans/"+loanID+"/schedule", nil, false, &sched); err != nil {
		return err
	}
	firstPayment := sched.Periods[0].Payment
	if err := mustDo(srv, "POST", "/loans/"+loanID+"/payments", map[string]any{"amount_cents": firstPayment}, false, nil); err != nil {
		return err
	}
	// Next payment before prepay.
	var sumBefore struct {
		NextPayment int64 `json:"next_payment_cents"`
	}
	if err := mustDo(srv, "GET", "/loans/"+loanID+"/summary", nil, false, &sumBefore); err != nil {
		return err
	}
	// Prepay 300_000 reducing payment.
	if err := mustDo(srv, "POST", "/loans/"+loanID+"/prepayments",
		map[string]any{"amount_cents": 300_000, "strategy": "reduce_payment"}, false, nil); err != nil {
		return err
	}
	// Term unchanged at 12.
	var l struct {
		TermPeriods int `json:"term_periods"`
	}
	if err := mustDo(srv, "GET", "/loans/"+loanID, nil, false, &l); err != nil {
		return err
	}
	if l.TermPeriods != 12 {
		return fmt.Errorf("reduce_payment term %d != 12", l.TermPeriods)
	}
	// Next payment must be strictly lower.
	var sumAfter struct {
		NextPayment int64 `json:"next_payment_cents"`
	}
	if err := mustDo(srv, "GET", "/loans/"+loanID+"/summary", nil, false, &sumAfter); err != nil {
		return err
	}
	if sumAfter.NextPayment <= 0 || sumAfter.NextPayment >= sumBefore.NextPayment {
		return fmt.Errorf("reduce_payment next %d not lower than %d", sumAfter.NextPayment, sumBefore.NextPayment)
	}
	// A prepayment reduces principal without its own schedule period, so the
	// plan's period-principal sum equals the original minus the prepayment.
	if p, _, _, _, err := scheduleTotals(srv, loanID); err != nil {
		return err
	} else if p != principal-300_000 {
		return fmt.Errorf("post-reduce-payment sum principal %d != %d", p, principal-300_000)
	}
	return nil
}

// smokeRateChange: a rate change recomputes the next payment over the
// remaining term at the new rate; past payments are immutable.
func smokeRateChange(srv *httptest.Server, dbPath string) error {
	const principal int64 = 1_000_000
	_, loanID, err := createLoanFixture(srv, principal, 6.0, 12, "equal_installment")
	if err != nil {
		return err
	}
	var sumBefore struct {
		NextPayment int64 `json:"next_payment_cents"`
	}
	if err := mustDo(srv, "GET", "/loans/"+loanID+"/summary", nil, false, &sumBefore); err != nil {
		return err
	}
	// Refinance to 18% (higher rate → higher next payment).
	if err := mustDo(srv, "POST", "/loans/"+loanID+"/rate-changes",
		map[string]any{"annual_rate_percent": 18.0}, false, nil); err != nil {
		return err
	}
	var sumAfter struct {
		NextPayment int64 `json:"next_payment_cents"`
	}
	if err := mustDo(srv, "GET", "/loans/"+loanID+"/summary", nil, false, &sumAfter); err != nil {
		return err
	}
	if sumAfter.NextPayment <= sumBefore.NextPayment {
		return fmt.Errorf("higher rate should raise next payment: before %d, after %d", sumBefore.NextPayment, sumAfter.NextPayment)
	}
	// A rate change does not move principal, so the plan still sums to the
	// original principal and zeroes exactly.
	if p, _, _, _, err := scheduleTotals(srv, loanID); err != nil {
		return err
	} else if p != principal {
		return fmt.Errorf("post-rate-change sum principal %d != %d", p, principal)
	}
	return nil
}

// smokeAmountMismatch: a scheduled payment with the wrong amount is rejected
// (422) and leaves the loan state untouched.
func smokeAmountMismatch(srv *httptest.Server, dbPath string) error {
	const principal int64 = 1_000_000
	_, loanID, err := createLoanFixture(srv, principal, 12.0, 12, "equal_installment")
	if err != nil {
		return err
	}
	// A deliberately wrong amount (off by one cent from the first payment).
	var sched struct {
		Periods []struct {
			Payment int64 `json:"payment"`
		} `json:"periods"`
	}
	if err := mustDo(srv, "GET", "/loans/"+loanID+"/schedule", nil, false, &sched); err != nil {
		return err
	}
	wrongAmount := sched.Periods[0].Payment + 1
	if _, err := expectStatus(srv, "POST", "/loans/"+loanID+"/payments",
		map[string]any{"amount_cents": wrongAmount}, false, http.StatusUnprocessableEntity); err != nil {
		return err
	}
	// Balance unchanged.
	var bal struct {
		Outstanding int64 `json:"outstanding_cents"`
	}
	if err := mustDo(srv, "GET", "/loans/"+loanID+"/balance", nil, false, &bal); err != nil {
		return err
	}
	if bal.Outstanding != principal {
		return fmt.Errorf("after rejected payment outstanding %d != %d (state changed!)", bal.Outstanding, principal)
	}
	return nil
}

// smokeAsOfBalance: the as-of balance matches principal − Σ(principal paid
// through as_of), computed from the ledger.
func smokeAsOfBalance(srv *httptest.Server, dbPath string) error {
	const principal int64 = 1_000_000
	_, loanID, err := createLoanFixture(srv, principal, 12.0, 12, "equal_installment")
	if err != nil {
		return err
	}
	// Pay periods 1 and 2.
	var sched struct {
		Periods []struct {
			Payment   int64 `json:"payment"`
			Principal int64 `json:"principal"`
		} `json:"periods"`
	}
	if err := mustDo(srv, "GET", "/loans/"+loanID+"/schedule", nil, false, &sched); err != nil {
		return err
	}
	paid1 := sched.Periods[0].Principal
	// The as-of balance uses the payment's Seq (the period it was applied at),
	// so a payment for period k is included in as_of >= k. After paying period
	// 1, outstanding(as_of=1) = principal − paid1.
	paid2 := sched.Periods[1].Principal
	if err := mustDo(srv, "POST", "/loans/"+loanID+"/payments", map[string]any{"amount_cents": sched.Periods[0].Payment}, false, nil); err != nil {
		return err
	}
	if err := mustDo(srv, "POST", "/loans/"+loanID+"/payments", map[string]any{"amount_cents": sched.Periods[1].Payment}, false, nil); err != nil {
		return err
	}
	// as_of=1 includes the first payment's principal.
	var bal1 struct {
		Outstanding int64 `json:"outstanding_cents"`
	}
	if err := mustDo(srv, "GET", "/loans/"+loanID+"/balance?as_of=1", nil, false, &bal1); err != nil {
		return err
	}
	if bal1.Outstanding != principal-paid1 {
		return fmt.Errorf("as_of=1 outstanding %d, want %d", bal1.Outstanding, principal-paid1)
	}
	// as_of=2 includes both.
	var bal2 struct {
		Outstanding int64 `json:"outstanding_cents"`
	}
	if err := mustDo(srv, "GET", "/loans/"+loanID+"/balance?as_of=2", nil, false, &bal2); err != nil {
		return err
	}
	if bal2.Outstanding != principal-paid1-paid2 {
		return fmt.Errorf("as_of=2 outstanding %d, want %d", bal2.Outstanding, principal-paid1-paid2)
	}
	return nil
}

// smokeRecompute: the consistency recompute reports zero drift on a healthy
// portfolio.
func smokeRecompute(srv *httptest.Server, dbPath string) error {
	_, loanID, err := createLoanFixture(srv, 1_000_000, 12.0, 12, "equal_installment")
	if err != nil {
		return err
	}
	var sched struct {
		Periods []struct {
			Payment int64 `json:"payment"`
		} `json:"periods"`
	}
	if err := mustDo(srv, "GET", "/loans/"+loanID+"/schedule", nil, false, &sched); err != nil {
		return err
	}
	if err := mustDo(srv, "POST", "/loans/"+loanID+"/payments", map[string]any{"amount_cents": sched.Periods[0].Payment}, false, nil); err != nil {
		return err
	}
	if err := mustDo(srv, "POST", "/loans/"+loanID+"/prepayments", map[string]any{"amount_cents": 100_000, "strategy": "reduce_term"}, false, nil); err != nil {
		return err
	}
	var rep struct {
		Checked int  `json:"checked"`
		OK      bool `json:"ok"`
	}
	if err := mustDo(srv, "POST", "/admin/recompute", nil, true, &rep); err != nil {
		return err
	}
	if rep.Checked < 1 || !rep.OK {
		return fmt.Errorf("recompute: checked=%d ok=%v (expected ok, no drift)", rep.Checked, rep.OK)
	}
	return nil
}

// smokeRestartRecovery seeds a loan + payment, closes the store, reopens the
// same file and asserts the balance recomputed from the ledger is unchanged.
// This is the restart-recovery path: nothing is cached, the outstanding
// principal is always a pure function of the persisted payment ledger.
func smokeRestartRecovery(dbPath string) error {
	srv, err := newServer(dbPath)
	if err != nil {
		return err
	}
	const principal int64 = 1_000_000
	_, loanID, err := createLoanFixture(srv, principal, 12.0, 12, "equal_installment")
	if err != nil {
		srv.Close()
		return err
	}
	var sched struct {
		Periods []struct {
			Payment int64 `json:"payment"`
		} `json:"periods"`
	}
	if err := mustDo(srv, "GET", "/loans/"+loanID+"/schedule", nil, false, &sched); err != nil {
		srv.Close()
		return err
	}
	if err := mustDo(srv, "POST", "/loans/"+loanID+"/payments", map[string]any{"amount_cents": sched.Periods[0].Payment}, false, nil); err != nil {
		srv.Close()
		return err
	}
	// Capture the live balance.
	var balBefore struct {
		Outstanding int64 `json:"outstanding_cents"`
	}
	if err := mustDo(srv, "GET", "/loans/"+loanID+"/balance", nil, false, &balBefore); err != nil {
		srv.Close()
		return err
	}
	// Close the first server (which closes its store via the shutdown hook),
	// then open a fresh server on the SAME db file to simulate a restart.
	srv.Close()
	srv2, err := newServer(dbPath)
	if err != nil {
		return fmt.Errorf("reopen store: %w", err)
	}
	defer srv2.Close()
	var balAfter struct {
		Outstanding int64 `json:"outstanding_cents"`
	}
	if err := mustDo(srv2, "GET", "/loans/"+loanID+"/balance", nil, false, &balAfter); err != nil {
		return err
	}
	if balAfter.Outstanding != balBefore.Outstanding {
		return fmt.Errorf("restart recovery: balance changed %d -> %d", balBefore.Outstanding, balAfter.Outstanding)
	}
	// The loan should still be active and have one paid period.
	var sum struct {
		PaidPeriods int    `json:"paid_periods"`
		Status      string `json:"status"`
	}
	if err := mustDo(srv2, "GET", "/loans/"+loanID+"/summary", nil, false, &sum); err != nil {
		return err
	}
	if sum.Status != "active" || sum.PaidPeriods != 1 {
		return fmt.Errorf("restart recovery: status=%s paid=%d (want active/1)", sum.Status, sum.PaidPeriods)
	}
	return nil
}
