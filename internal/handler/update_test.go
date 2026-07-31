package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	models "github.com/rebusman/svcmetrics/internal/model"
	"github.com/rebusman/svcmetrics/internal/storage"
)

func newTestRouter(s storage.Storage) chi.Router {
	r := chi.NewRouter()
	r.Post("/update", UpdateJSONHandler(s))
	r.Post("/update/{type}/{name}/{value}", UpdateHandler(s))
	r.Get("/value/{type}/{name}", ValueHandler(s))
	r.Post("/value", ValueJSONHandler(s))
	r.Get("/", ListHandler(s))
	return r
}

func TestUpdateHandlerGauge(t *testing.T) {
	s := storage.NewMemStorage()
	r := newTestRouter(s)

	req := httptest.NewRequest(http.MethodPost, "/update/gauge/Alloc/12.5", nil)
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	got, err := s.GetGauge(context.Background(), "Alloc")
	if err != nil {
		t.Fatalf("GetGauge error = %v", err)
	}
	if got != 12.5 {
		t.Fatalf("gauge value = %v, want 12.5", got)
	}
}

func TestUpdateHandlerCounter(t *testing.T) {
	s := storage.NewMemStorage()
	r := newTestRouter(s)

	req := httptest.NewRequest(http.MethodPost, "/update/counter/PollCount/3", nil)
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)

	req2 := httptest.NewRequest(http.MethodPost, "/update/counter/PollCount/3", nil)
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec2.Code, http.StatusOK)
	}

	got, err := s.GetCounter(context.Background(), "PollCount")
	if err != nil {
		t.Fatalf("GetCounter error = %v", err)
	}
	if got != 6 {
		t.Fatalf("counter value = %d, want 6", got)
	}
}

func TestUpdateJSONHandler(t *testing.T) {
	s := storage.NewMemStorage()
	r := newTestRouter(s)

	t.Run("gauge ok", func(t *testing.T) {
		val := 1744184459.0
		m := models.Metrics{ID: "LastGC", MType: models.Gauge, Value: &val}
		body, _ := json.Marshal(m)

		req := httptest.NewRequest(http.MethodPost, "/update", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("Content-Type = %s, want application/json", ct)
		}
		var result models.Metrics
		if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if result.ID != "LastGC" || result.MType != models.Gauge || result.Value == nil || *result.Value != val {
			t.Errorf("unexpected response: %+v", result)
		}
	})

	t.Run("counter ok", func(t *testing.T) {
		delta := int64(5)
		m := models.Metrics{ID: "PollCount", MType: models.Counter, Delta: &delta}
		body, _ := json.Marshal(m)

		req := httptest.NewRequest(http.MethodPost, "/update", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		var result models.Metrics
		if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if result.Delta == nil || *result.Delta != 5 {
			t.Errorf("unexpected delta: %v", result.Delta)
		}
	})

	t.Run("missing id", func(t *testing.T) {
		m := models.Metrics{MType: models.Gauge}
		body, _ := json.Marshal(m)
		req := httptest.NewRequest(http.MethodPost, "/update", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("invalid type", func(t *testing.T) {
		val := 1.0
		m := models.Metrics{ID: "Test", MType: "unknown", Value: &val}
		body, _ := json.Marshal(m)
		req := httptest.NewRequest(http.MethodPost, "/update", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("empty body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/update", bytes.NewReader([]byte{}))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})
}
