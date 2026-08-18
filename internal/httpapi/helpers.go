package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"task109-loanamort/internal/amort"
	"task109-loanamort/internal/loan"
)

// decode reads a JSON body into v. An empty body is allowed (v stays zero) so
// endpoints with no required fields can be called with no body; a malformed
// non-empty body is a 400.
func decode(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, v)
}

// writeJSON sets the content type and writes v as JSON.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a JSON error envelope.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeServiceError maps a loan sentinel error to an HTTP status. Unknown
// errors fall back to 500.
func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, loan.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, loan.ErrTerminal):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, loan.ErrConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, loan.ErrAmountMismatch):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, loan.ErrPrepayTooLarge):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, loan.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// atoiOr parses s as int, returning def on empty/parse error.
func atoiOr(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := parseIntStrict(s)
	if err != nil {
		return 0
	}
	return n
}

// parseIntStrict parses s as a base-10 int.
func parseIntStrict(s string) (int, error) {
	var n int
	neg := false
	if len(s) > 0 && (s[0] == '-' || s[0] == '+') {
		neg = s[0] == '-'
		s = s[1:]
	}
	if len(s) == 0 {
		return 0, errors.New("empty")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("invalid")
		}
		n = n*10 + int(c-'0')
	}
	if neg {
		n = -n
	}
	return n, nil
}

// accruedInterestFromSchedule sums scheduled interest through asOf. asOf < 0
// means "all periods".
func accruedInterestFromSchedule(periods []amort.Period, asOf int) int64 {
	var sum int64
	for i := range periods {
		if asOf < 0 || periods[i].Number <= asOf {
			sum += periods[i].Interest
		}
	}
	return sum
}
