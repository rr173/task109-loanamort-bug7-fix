package httpapi

import (
	"net/http"

	"task109-loanamort/internal/loan"
)

func handleCashflow(s *loan.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp, err := s.Cashflow(r.Context(), r.PathValue("id"))
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}
