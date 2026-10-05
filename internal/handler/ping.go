package handler

import (
	"context"
	"net/http"
	"time"
)

//go:generate go tool mockgen -destination=../mocks/pinger.go -package=mocks github.com/rebusman/svcmetrics/internal/handler Pinger

// Pinger reports whether the backing database is reachable. Only the
// PostgreSQL storage implements it.
type Pinger interface {
	// PingContext checks the connection to the database within ctx and
	// returns nil if it is alive.
	PingContext(ctx context.Context) error
}

// PingHandler handles GET /ping. It answers 500 when no database is configured
// or when the ping fails.
func PingHandler(p Pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		if p == nil {
			http.Error(w, "Database is not configured", http.StatusInternalServerError)
			return
		}

		if err := p.PingContext(ctx); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
	}
}
