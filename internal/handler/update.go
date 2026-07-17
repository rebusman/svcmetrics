package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	models "github.com/rebusman/svcmetrics/internal/model"
)

// Storage describes the metric storage used by the handler.
type ReadStorage interface {
	GetGauge(name string) (float64, error)
	GetCounter(name string) (int64, error)
	GetAllGauges() map[string]float64
	GetAllCounters() map[string]int64
}

type WriteStorage interface {
	UpdateGauge(name string, value float64)
	UpdateCounter(name string, value int64)
}

type Storage interface {
	ReadStorage
	WriteStorage
}

// UpdateJSONHandler handles POST /update.
func UpdateJSONHandler(s Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var m models.Metrics
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			if err.Error() == "EOF" {
				http.Error(w, "Empty body", http.StatusBadRequest)
			} else {
				http.Error(w, "Invalid JSON", http.StatusBadRequest)
			}
			return
		}

		if m.ID == "" {
			http.Error(w, "Metric ID missing", http.StatusBadRequest)
			return
		}

		var result models.Metrics
		result.ID = m.ID
		result.MType = m.MType

		switch m.MType {
		case models.Gauge:
			if m.Value == nil {
				http.Error(w, "Value missing for gauge", http.StatusBadRequest)
				return
			}
			s.UpdateGauge(m.ID, *m.Value)
			val, _ := s.GetGauge(m.ID)
			result.Value = &val
		case models.Counter:
			if m.Delta == nil {
				http.Error(w, "Delta missing for counter", http.StatusBadRequest)
				return
			}
			s.UpdateCounter(m.ID, *m.Delta)
			val, _ := s.GetCounter(m.ID)
			result.Delta = &val
		default:
			http.Error(w, "Invalid metric type", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(result); err != nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	}
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
