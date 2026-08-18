// Command task109-loanamort serves the loan-amortization HTTP API backed by
// SQLite, and provides a --smoke-test that exercises the full amortization
// contract (schedule exactness, tail correction, zero rate, prepayment
// reduce_term/reduce_payment, rate-change refinance, as-of balance,
// restart recovery, transactional consistency) without real-time sleeps.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"task109-loanamort/internal/httpapi"
	"task109-loanamort/internal/loan"
	"task109-loanamort/internal/selfcheck"
	"task109-loanamort/internal/store"
)

// DefaultAdminToken protects admin endpoints. Override with LOAN_ADMIN_TOKEN.
const DefaultAdminToken = "admin-secret"

func main() {
	smoke := flag.Bool("smoke-test", false, "run self-check and exit")
	dbPath := flag.String("db", "loans.db", "SQLite database file path")
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	if *smoke {
		if err := selfcheck.Run(); err != nil {
			fmt.Println("smoke-test: FAIL:", err)
			osExit(1)
		}
		fmt.Println("smoke-test: ok")
		osExit(0)
	}

	adminToken := os.Getenv("LOAN_ADMIN_TOKEN")
	if adminToken == "" {
		adminToken = DefaultAdminToken
	}

	st, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	svc := loan.New(st)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           httpapi.NewMux(svc, adminToken),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Printf("loan service %s listening on %s (db=%s)", httpapi.Version, *addr, *dbPath)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

// osExit is indirected so tests can substitute it; in production it is os.Exit.
var osExit = os.Exit
