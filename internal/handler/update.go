package handler

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	models "github.com/rebusman/svcmetrics/internal/model"
)

// Storage describes the metric storage used by the handler.
type Storage interface {
	UpdateGauge(name string, value float64)
	UpdateCounter(name string, value int64)
	GetGauge(name string) (float64, error)
	GetCounter(name string) (int64, error)
	GetAllGauges() map[string]float64
	GetAllCounters() map[string]int64
}

// UpdateHandler handles POST /update/{type}/{name}/{value}.
func UpdateHandler(s Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mType := chi.URLParam(r, "type")
		mName := chi.URLParam(r, "name")
		mValueStr := chi.URLParam(r, "value")

		if mName == "" {
			http.Error(w, "Metric name missing", http.StatusNotFound)
			return
		}

		switch mType {
		case models.Gauge:
			val, err := strconv.ParseFloat(mValueStr, 64)
			if err != nil {
				http.Error(w, "Invalid gauge value", http.StatusBadRequest)
				return
			}
			s.UpdateGauge(mName, val)
		case models.Counter:
			val, err := strconv.ParseInt(mValueStr, 10, 64)
			if err != nil {
				http.Error(w, "Invalid counter value", http.StatusBadRequest)
				return
			}
			s.UpdateCounter(mName, val)
		default:
			http.Error(w, "Invalid metric type", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
	}
}
