package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	models "github.com/rebusman/svcmetrics/internal/model"
	"github.com/rebusman/svcmetrics/internal/storage"
)

// writeStorageError replies 404 for a missing metric and 500 for any other
// storage failure.
func writeStorageError(w http.ResponseWriter, err error) {
	if errors.Is(err, models.ErrNotFound) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

// UpdateJSONHandler handles POST /update.
func UpdateJSONHandler(s storage.Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var m models.Metrics
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
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

		ctx := r.Context()

		var result models.Metrics
		result.ID = m.ID
		result.MType = m.MType

		switch m.MType {
		case models.Gauge:
			if m.Value == nil {
				http.Error(w, "Value missing for gauge", http.StatusBadRequest)
				return
			}

			val, err := s.UpdateGauge(ctx, m.ID, *m.Value)
			if err != nil {
				writeStorageError(w, err)
				return
			}
			result.Value = &val
		case models.Counter:
			if m.Delta == nil {
				http.Error(w, "Delta missing for counter", http.StatusBadRequest)
				return
			}
			val, err := s.UpdateCounter(ctx, m.ID, *m.Delta)
			if err != nil {
				writeStorageError(w, err)
				return
			}
			result.Delta = &val
		default:
			http.Error(w, "Invalid metric type", http.StatusBadRequest)
			return
		}

		// Same reason as in ValueJSONHandler: commit the response only once the
		// body is known to be encodable.
		var payload bytes.Buffer
		if err := json.NewEncoder(&payload).Encode(result); err != nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Response already committed; the write error is logged by the
		// ResponseWriter wrapper in cmd/server.
		_, _ = payload.WriteTo(w)
	}
}

// UpdateHandler handles POST /update/{type}/{name}/{value}.
func UpdateHandler(s storage.Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mType := chi.URLParam(r, "type")
		mName := chi.URLParam(r, "name")
		mValueStr := chi.URLParam(r, "value")

		if mName == "" {
			http.Error(w, "Metric name missing", http.StatusNotFound)
			return
		}

		ctx := r.Context()

		switch mType {
		case models.Gauge:
			val, err := strconv.ParseFloat(mValueStr, 64)
			if err != nil {
				http.Error(w, "Invalid gauge value", http.StatusBadRequest)
				return
			}
			if _, err := s.UpdateGauge(ctx, mName, val); err != nil {
				writeStorageError(w, err)
				return
			}
		case models.Counter:
			val, err := strconv.ParseInt(mValueStr, 10, 64)
			if err != nil {
				http.Error(w, "Invalid counter value", http.StatusBadRequest)
				return
			}
			if _, err := s.UpdateCounter(ctx, mName, val); err != nil {
				writeStorageError(w, err)
				return
			}
		default:
			http.Error(w, "Invalid metric type", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
	}
}
