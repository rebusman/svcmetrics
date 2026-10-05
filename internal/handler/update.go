// Package handler implements the HTTP handlers of the metrics server together
// with the gzip middleware they rely on.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	models "github.com/rebusman/svcmetrics/internal/model"
	"github.com/rebusman/svcmetrics/internal/repository"
)

// writeStorageError replies 404 for a missing metric, 400 for a malformed one,
// 504 for a request that ran past the deadline the router set and 500 for any
// other storage failure.
func writeStorageError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, models.ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, models.ErrInvalidMetric):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, context.DeadlineExceeded):
		http.Error(w, err.Error(), http.StatusGatewayTimeout)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// UpdateJSONHandler handles POST /update: a single metric in JSON. It answers
// with the stored metric, 400 for a malformed request and 500 for a storage
// failure. A stored metric is reported to a, which may be nil.
func UpdateJSONHandler(s repository.Storage, a Auditor) http.HandlerFunc {
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

		notifyAudit(a, r, []string{m.ID})

		var payload bytes.Buffer
		if err := json.NewEncoder(&payload).Encode(result); err != nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = payload.WriteTo(w)
	}
}

// UpdatesJSONHandler handles POST /updates/: a batch of metrics stored in a
// single atomic write. An empty batch is accepted as a no-op, a malformed one
// (including a bare null instead of an array) is rejected in full with 400. The
// single-metric endpoints keep working alongside it. A stored batch is reported
// to a, which may be nil; an empty one stores nothing and is not reported.
func UpdatesJSONHandler(s repository.Storage, a Auditor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var batch []models.Metrics
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
			if errors.Is(err, io.EOF) {
				http.Error(w, "Empty body", http.StatusBadRequest)
			} else {
				http.Error(w, "Invalid JSON", http.StatusBadRequest)
			}
			return
		}

		// json.Decode accepts a bare null into a slice, leaving it nil: the
		// endpoint contract is a JSON array, so such a body is rejected. An
		// empty array [] decodes to a non-nil slice and stays valid.
		if batch == nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}

		if len(batch) > 0 {
			if err := s.UpdateBatch(r.Context(), batch); err != nil {
				writeStorageError(w, err)
				return
			}

			names := make([]string, len(batch))
			for i, m := range batch {
				names[i] = m.ID
			}
			notifyAudit(a, r, names)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}
}

// UpdateHandler handles POST /update/{type}/{name}/{value}: a single metric
// passed in the path. A stored metric is reported to a, which may be nil.
func UpdateHandler(s repository.Storage, a Auditor) http.HandlerFunc {
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

		notifyAudit(a, r, []string{mName})

		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
	}
}
