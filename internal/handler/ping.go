package handler

import (
	"context"
	"net/http"
	"time"
)

type Pinger interface {
	PingContext(ctx context.Context) error
}

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
