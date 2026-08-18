package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"task109-loanamort/internal/httpapi"
	"task109-loanamort/internal/loan"
	"task109-loanamort/internal/store"
)

func TestEmptyGlobalPaymentsEncodeAsArray(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/loan.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = context.Background()
	r := httptest.NewRequest("GET", "/payments", nil)
	w := httptest.NewRecorder()
	httpapi.NewMux(loan.New(db), "").ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
	var body struct {
		Payments json.RawMessage `json:"payments"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if string(body.Payments) != "[]" {
		t.Fatalf("payments=%s, want []", body.Payments)
	}
}
