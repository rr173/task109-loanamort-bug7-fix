package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"task109-loanamort/internal/loan"
	"task109-loanamort/internal/store"
)

// newTestMux opens a fresh SQLite file and returns a mux + the admin token the
// tests must present for admin endpoints.
func newTestMux(t *testing.T) http.Handler {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	svc := loan.New(st)
	return NewMux(svc, testAdminToken)
}

const testAdminToken = "admin-secret"

func do(t *testing.T, h http.Handler, method, target string, body any, admin bool) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == nil {
		r = httptest.NewRequest(method, target, nil)
	} else {
		buf, _ := json.Marshal(body)
		r = httptest.NewRequest(method, target, bytes.NewReader(buf))
		r.Header.Set("Content-Type", "application/json")
	}
	if admin {
		r.Header.Set(AdminTokenHeader, testAdminToken)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func mustStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status %d, want %d: %s", w.Code, want, w.Body.String())
	}
}

// createFixture creates a borrower and a loan, returning their ids.
func createFixture(t *testing.T, h http.Handler, principal int64, annual float64, periods int, ltype string) (string, string) {
	t.Helper()
	w := do(t, h, "POST", "/borrowers", map[string]any{"name": "alice"}, false)
	mustStatus(t, w, http.StatusCreated)
	var b struct {
		BorrowerID string `json:"borrower_id"`
	}
	json.Unmarshal(w.Body.Bytes(), &b)
	w2 := do(t, h, "POST", "/loans", map[string]any{
		"borrower_id": b.BorrowerID, "principal_cents": principal,
		"annual_rate_percent": annual, "periods": periods, "type": ltype,
	}, false)
	mustStatus(t, w2, http.StatusCreated)
	var l struct {
		LoanID string `json:"loan_id"`
	}
	json.Unmarshal(w2.Body.Bytes(), &l)
	return b.BorrowerID, l.LoanID
}

func TestHealthz(t *testing.T) {
	h := newTestMux(t)
	w := do(t, h, "GET", "/healthz", nil, false)
	mustStatus(t, w, http.StatusOK)
	var v struct {
		Status string `json:"status"`
	}
	json.Unmarshal(w.Body.Bytes(), &v)
	if v.Status != "ok" {
		t.Errorf("status = %q", v.Status)
	}
}

func TestCreateLoanAndSchedule(t *testing.T) {
	h := newTestMux(t)
	_, loanID := createFixture(t, h, 1_000_000, 12.0, 12, "equal_installment")
	w := do(t, h, "GET", "/loans/"+loanID+"/schedule", nil, false)
	mustStatus(t, w, http.StatusOK)
	var sched struct {
		Periods []struct {
			Principal int64 `json:"principal"`
			Balance   int64 `json:"balance"`
		} `json:"periods"`
		TotalPrincipal int64 `json:"total_principal_cents"`
	}
	json.Unmarshal(w.Body.Bytes(), &sched)
	if sched.TotalPrincipal != 1_000_000 {
		t.Errorf("total principal %d != 1000000", sched.TotalPrincipal)
	}
	if sched.Periods[len(sched.Periods)-1].Balance != 0 {
		t.Errorf("final balance %d != 0", sched.Periods[len(sched.Periods)-1].Balance)
	}
}

func TestRecordPaymentViaHTTP(t *testing.T) {
	h := newTestMux(t)
	_, loanID := createFixture(t, h, 1_000_000, 12.0, 12, "equal_installment")
	// Get the first scheduled payment.
	w := do(t, h, "GET", "/loans/"+loanID+"/schedule", nil, false)
	var sched struct {
		Periods []struct {
			Payment int64 `json:"payment"`
		} `json:"periods"`
	}
	json.Unmarshal(w.Body.Bytes(), &sched)
	w2 := do(t, h, "POST", "/loans/"+loanID+"/payments", map[string]any{"amount_cents": sched.Periods[0].Payment}, false)
	mustStatus(t, w2, http.StatusCreated)
	// Balance reduced.
	w3 := do(t, h, "GET", "/loans/"+loanID+"/balance", nil, false)
	mustStatus(t, w3, http.StatusOK)
	var bal struct {
		Outstanding int64 `json:"outstanding_cents"`
	}
	json.Unmarshal(w3.Body.Bytes(), &bal)
	if bal.Outstanding <= 0 || bal.Outstanding >= 1_000_000 {
		t.Errorf("outstanding %d not in (0,1000000)", bal.Outstanding)
	}
}

func TestAmountMismatchReturns422(t *testing.T) {
	h := newTestMux(t)
	_, loanID := createFixture(t, h, 1_000_000, 12.0, 12, "equal_installment")
	w := do(t, h, "POST", "/loans/"+loanID+"/payments", map[string]any{"amount_cents": 1}, false)
	mustStatus(t, w, http.StatusUnprocessableEntity)
}

func TestPrepayReduceTermViaHTTP(t *testing.T) {
	h := newTestMux(t)
	_, loanID := createFixture(t, h, 1_000_000, 12.0, 12, "equal_installment")
	w := do(t, h, "GET", "/loans/"+loanID+"/schedule", nil, false)
	var sched struct {
		Periods []struct {
			Payment int64 `json:"payment"`
		} `json:"periods"`
	}
	json.Unmarshal(w.Body.Bytes(), &sched)
	do(t, h, "POST", "/loans/"+loanID+"/payments", map[string]any{"amount_cents": sched.Periods[0].Payment}, false)
	w2 := do(t, h, "POST", "/loans/"+loanID+"/prepayments", map[string]any{"amount_cents": 200_000, "strategy": "reduce_term"}, false)
	mustStatus(t, w2, http.StatusCreated)
	var resp struct {
		Loan struct {
			TermPeriods int `json:"term_periods"`
		} `json:"loan"`
	}
	json.Unmarshal(w2.Body.Bytes(), &resp)
	if resp.Loan.TermPeriods <= 1 || resp.Loan.TermPeriods >= 12 {
		t.Errorf("reduce_term term %d not in (1,12)", resp.Loan.TermPeriods)
	}
}

func TestRateChangeViaHTTP(t *testing.T) {
	h := newTestMux(t)
	_, loanID := createFixture(t, h, 1_000_000, 6.0, 12, "equal_installment")
	w := do(t, h, "POST", "/loans/"+loanID+"/rate-changes", map[string]any{"annual_rate_percent": 18.0}, false)
	mustStatus(t, w, http.StatusOK)
	w2 := do(t, h, "GET", "/loans/"+loanID+"/summary", nil, false)
	var sum struct {
		NextPayment int64 `json:"next_payment_cents"`
	}
	json.Unmarshal(w2.Body.Bytes(), &sum)
	if sum.NextPayment <= 0 {
		t.Errorf("next payment %d not positive", sum.NextPayment)
	}
}

func TestNotFoundReturns404(t *testing.T) {
	h := newTestMux(t)
	w := do(t, h, "GET", "/loans/nope", nil, false)
	mustStatus(t, w, http.StatusNotFound)
}

func TestAdminRecomputeRequiresToken(t *testing.T) {
	h := newTestMux(t)
	// Without the admin token → 401.
	w := do(t, h, "POST", "/admin/recompute", nil, false)
	mustStatus(t, w, http.StatusUnauthorized)
	// With the token → 200.
	w2 := do(t, h, "POST", "/admin/recompute", nil, true)
	mustStatus(t, w2, http.StatusOK)
}

func TestListLoansFilterByStatus(t *testing.T) {
	h := newTestMux(t)
	createFixture(t, h, 1_000_000, 12.0, 12, "equal_installment")
	createFixture(t, h, 500_000, 6.0, 6, "equal_principal")
	w := do(t, h, "GET", "/loans?status=active", nil, false)
	mustStatus(t, w, http.StatusOK)
	var resp struct {
		Count int `json:"count"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Count != 2 {
		t.Errorf("active loans = %d, want 2", resp.Count)
	}
}

func TestStatsEndpoint(t *testing.T) {
	h := newTestMux(t)
	createFixture(t, h, 1_000_000, 12.0, 12, "equal_installment")
	w := do(t, h, "GET", "/stats", nil, false)
	mustStatus(t, w, http.StatusOK)
	var s struct {
		TotalLoans     int `json:"total_loans"`
		TotalBorrowers int `json:"total_borrowers"`
	}
	json.Unmarshal(w.Body.Bytes(), &s)
	if s.TotalLoans != 1 || s.TotalBorrowers != 1 {
		t.Errorf("stats: loans=%d borrowers=%d", s.TotalLoans, s.TotalBorrowers)
	}
}

func TestGlobalPaymentsEndpoint(t *testing.T) {
	h := newTestMux(t)
	_, loanID := createFixture(t, h, 1_000_000, 12.0, 12, "equal_installment")
	w := do(t, h, "GET", "/loans/"+loanID+"/schedule", nil, false)
	var sched struct {
		Periods []struct {
			Payment int64 `json:"payment"`
		} `json:"periods"`
	}
	json.Unmarshal(w.Body.Bytes(), &sched)
	do(t, h, "POST", "/loans/"+loanID+"/payments", map[string]any{"amount_cents": sched.Periods[0].Payment}, false)
	w2 := do(t, h, "GET", "/payments", nil, false)
	mustStatus(t, w2, http.StatusOK)
	var resp struct {
		Count int `json:"count"`
	}
	json.Unmarshal(w2.Body.Bytes(), &resp)
	if resp.Count != 1 {
		t.Errorf("global payments = %d, want 1", resp.Count)
	}
}

func TestBorrowerLoansEndpoint(t *testing.T) {
	h := newTestMux(t)
	bid, _ := createFixture(t, h, 1_000_000, 12.0, 12, "equal_installment")
	w := do(t, h, "GET", "/borrowers/"+bid+"/loans", nil, false)
	mustStatus(t, w, http.StatusOK)
	var resp struct {
		Count int `json:"count"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Count != 1 {
		t.Errorf("borrower loans = %d, want 1", resp.Count)
	}
}
