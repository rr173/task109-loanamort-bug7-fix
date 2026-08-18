// Package httpapi wires the loan Service to HTTP/JSON endpoints. Handlers
// are plain functions over *loan.Service so the same mux is shared by the real
// server, the smoke test and the handler tests (httptest).
//
// Routes are registered as Go 1.22 ServeMux patterns ("METHOD /path"). Every
// mutating endpoint validates its JSON body via decode; business errors are
// mapped to status codes by writeServiceError.
package httpapi

import (
	"net/http"
	"strconv"

	"task109-loanamort/internal/loan"
)

// Version is the service identifier reported by /healthz and /version.
const Version = "task109-loanamort/v1"

// NewMux builds the HTTP handler tree over the given service. adminToken is a
// shared secret required by admin endpoints (POST /admin/recompute); empty
// disables admin protection (used by the smoke test).
func NewMux(svc *loan.Service, adminToken string) http.Handler {
	mux := http.NewServeMux()
	requireAdmin := func(w http.ResponseWriter, r *http.Request) bool {
		if adminToken != "" && r.Header.Get(AdminTokenHeader) != adminToken {
			writeError(w, http.StatusUnauthorized, "invalid admin token")
			return false
		}
		return true
	}

	// --- health / version ---
	mux.HandleFunc("GET /healthz", handleHealth)
	mux.HandleFunc("GET /version", handleVersion)

	// --- borrowers ---
	mux.HandleFunc("POST /borrowers", handleCreateBorrower(svc))
	mux.HandleFunc("GET /borrowers", handleListBorrowers(svc))
	mux.HandleFunc("GET /borrowers/{id}", handleGetBorrower(svc))
	mux.HandleFunc("GET /borrowers/{id}/loans", handleBorrowerLoans(svc))

	// --- loans ---
	mux.HandleFunc("POST /loans", handleCreateLoan(svc))
	mux.HandleFunc("GET /loans", handleListLoans(svc))
	mux.HandleFunc("GET /loans/{id}", handleGetLoan(svc))
	mux.HandleFunc("PATCH /loans/{id}", handleCancelLoan(svc))
	mux.HandleFunc("GET /loans/{id}/schedule", handleSchedule(svc))
	mux.HandleFunc("GET /loans/{id}/balance", handleBalance(svc))
	mux.HandleFunc("GET /loans/{id}/payoff", handlePayoff(svc))
	mux.HandleFunc("GET /loans/{id}/summary", handleSummary(svc))
	mux.HandleFunc("GET /loans/{id}/projection", handleProjection(svc))
	mux.HandleFunc("GET /loans/{id}/cashflow", handleCashflow(svc))
	mux.HandleFunc("GET /loans/{id}/accrued-interest", handleAccruedInterest(svc))

	// --- payments ---
	mux.HandleFunc("POST /loans/{id}/payments", handleRecordPayment(svc))
	mux.HandleFunc("POST /loans/{id}/prepayments", handlePrepay(svc))
	mux.HandleFunc("POST /loans/{id}/rate-changes", handleRateChange(svc))
	mux.HandleFunc("GET /loans/{id}/payments", handleListPayments(svc))
	mux.HandleFunc("GET /loans/{id}/payments/{pid}", handleGetPayment(svc))
	mux.HandleFunc("GET /payments", handleGlobalPayments(svc))

	// --- stats / admin ---
	mux.HandleFunc("GET /stats", handleStats(svc))
	mux.HandleFunc("POST /admin/recompute", func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(w, r) {
			return
		}
		handleRecompute(svc)(w, r)
	})

	return mux
}

// AdminTokenHeader is the header consulted by admin-only endpoints.
const AdminTokenHeader = "X-Admin-Token"

// --- health / version ---

func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": Version})
}

func handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": Version})
}

// --- borrowers ---

func handleCreateBorrower(s *loan.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req loan.CreateBorrowerRequest
		if err := decode(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		b, err := s.CreateBorrower(r.Context(), req)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, b)
	}
}

func handleListBorrowers(s *loan.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		out, err := s.ListBorrowers(r.Context(), limit)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"borrowers": out, "count": len(out)})
	}
}

func handleGetBorrower(s *loan.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		b, err := s.GetBorrower(r.Context(), id)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, b)
	}
}

func handleBorrowerLoans(s *loan.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		out, err := s.BorrowerLoans(r.Context(), id)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"borrower_id": id, "loans": out, "count": len(out)})
	}
}

// --- loans ---

func handleCreateLoan(s *loan.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req loan.CreateLoanRequest
		if err := decode(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		l, err := s.CreateLoan(r.Context(), req)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, l)
	}
}

func handleListLoans(s *loan.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f := loan.ListLoansFilter{
			BorrowerID: q.Get("borrower_id"),
			Limit:      atoiOr(q.Get("limit"), 0),
		}
		if v := q.Get("status"); v != "" {
			f.Status = loan.LoanStatus(v)
		}
		if v := q.Get("type"); v != "" {
			f.Type = loan.LoanType(v)
		}
		out, err := s.ListLoans(r.Context(), f)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"loans": out, "count": len(out)})
	}
}

func handleGetLoan(s *loan.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		l, err := s.GetLoan(r.Context(), r.PathValue("id"))
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, l)
	}
}

func handleCancelLoan(s *loan.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		l, err := s.CancelLoan(r.Context(), r.PathValue("id"))
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, l)
	}
}

func handleSchedule(s *loan.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp, err := s.Schedule(r.Context(), r.PathValue("id"))
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func handleBalance(s *loan.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		asOf, valid := requireNonNegativeQuery(r, "as_of")
		if !valid {
			writeError(w, http.StatusBadRequest, "as_of must be a non-negative integer")
			return
		}
		resp, err := s.Balance(r.Context(), r.PathValue("id"), asOf)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func handlePayoff(s *loan.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		asOf, valid := requireNonNegativeQuery(r, "as_of")
		if !valid {
			writeError(w, http.StatusBadRequest, "as_of must be a non-negative integer")
			return
		}
		resp, err := s.Payoff(r.Context(), r.PathValue("id"), asOf)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func handleSummary(s *loan.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp, err := s.Summary(r.Context(), r.PathValue("id"))
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func handleProjection(s *loan.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit := atoiOr(r.URL.Query().Get("periods"), 0)
		resp, err := s.Projection(r.Context(), r.PathValue("id"), limit)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func handleAccruedInterest(s *loan.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		asOf := atoiOr(r.URL.Query().Get("as_of"), 0)
		resp, err := s.Schedule(r.Context(), id)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		accrued := accruedInterestFromSchedule(resp.Periods, asOf)
		writeJSON(w, http.StatusOK, map[string]any{
			"loan_id":                id,
			"as_of":                  asOf,
			"accrued_interest_cents": accrued,
		})
	}
}

// --- payments ---

func handleRecordPayment(s *loan.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req loan.RecordPaymentRequest
		if err := decode(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		pay, l, err := s.RecordPayment(r.Context(), r.PathValue("id"), req)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"payment": pay, "loan": l})
	}
}

func handlePrepay(s *loan.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req loan.PrepayRequest
		if err := decode(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		pay, l, err := s.Prepay(r.Context(), r.PathValue("id"), req)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"payment": pay, "loan": l})
	}
}

func handleRateChange(s *loan.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req loan.ChangeRateRequest
		if err := decode(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		l, err := s.ChangeRate(r.Context(), r.PathValue("id"), req)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, l)
	}
}

func handleListPayments(s *loan.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		out, err := s.ListPaymentsByLoan(r.Context(), id)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"loan_id": id, "payments": out, "count": len(out)})
	}
}

func handleGetPayment(s *loan.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := s.GetPayment(r.Context(), r.PathValue("id"), r.PathValue("pid"))
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, p)
	}
}

func handleGlobalPayments(s *loan.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		out, err := s.ListPayments(r.Context(), loan.ListPaymentsFilter{
			LoanID:     q.Get("loan_id"),
			BorrowerID: q.Get("borrower_id"),
			Limit:      atoiOr(q.Get("limit"), 0),
		})
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"payments": out, "count": len(out)})
	}
}

// --- stats / admin ---

func handleStats(s *loan.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp, err := s.Stats(r.Context())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func handleRecompute(s *loan.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rep, err := s.Recompute(r.Context())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, rep)
	}
}
