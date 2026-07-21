package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	models "github.com/rebusman/svcmetrics/internal/model"
	"github.com/rebusman/svcmetrics/internal/storage"
)

func newTestRouter(s Storage) chi.Router {
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

	got, err := s.GetGauge("Alloc")
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

	got, err := s.GetCounter("PollCount")
	if err != nil {
		t.Fatalf("GetCounter error = %v", err)
	}
	if got != 6 {
		t.Fatalf("counter value = %d, want 6", got)
	}
}

func TestValueHandler(t *testing.T) {
	s := storage.NewMemStorage()
	s.UpdateGauge("Alloc", 12.5)
	s.UpdateCounter("PollCount", 5)
	r := newTestRouter(s)

	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantBody   string
	}{
		{"gauge ok", "/value/gauge/Alloc", http.StatusOK, "12.5"},
		{"counter ok", "/value/counter/PollCount", http.StatusOK, "5"},
		{"gauge not found", "/value/gauge/Unknown", http.StatusNotFound, ""},
		{"counter not found", "/value/counter/Unknown", http.StatusNotFound, ""},
		{"invalid type", "/value/unknown/Alloc", http.StatusBadRequest, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusOK && rec.Body.String() != tt.wantBody {
				t.Errorf("body = %s, want %s", rec.Body.String(), tt.wantBody)
			}
		})
	}
}

func TestListHandler(t *testing.T) {
	s := storage.NewMemStorage()
	s.UpdateGauge("Alloc", 12.5)
	r := newTestRouter(s)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Header().Get("Content-Type") != "text/html" {
		t.Errorf("content type = %s, want text/html", rec.Header().Get("Content-Type"))
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

func TestValueJSONHandler(t *testing.T) {
	s := storage.NewMemStorage()
	gaugeVal := 12.5
	s.UpdateGauge("Alloc", gaugeVal)
	s.UpdateCounter("PollCount", 7)
	r := newTestRouter(s)

	t.Run("gauge ok", func(t *testing.T) {
		m := models.Metrics{ID: "Alloc", MType: models.Gauge}
		body, _ := json.Marshal(m)
		req := httptest.NewRequest(http.MethodPost, "/value", bytes.NewReader(body))
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
		if result.Value == nil || *result.Value != gaugeVal {
			t.Errorf("value = %v, want %v", result.Value, gaugeVal)
		}
	})

	t.Run("counter ok", func(t *testing.T) {
		m := models.Metrics{ID: "PollCount", MType: models.Counter}
		body, _ := json.Marshal(m)
		req := httptest.NewRequest(http.MethodPost, "/value", bytes.NewReader(body))
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
		if result.Delta == nil || *result.Delta != 7 {
			t.Errorf("delta = %v, want 7", result.Delta)
		}
	})

	t.Run("not found", func(t *testing.T) {
		m := models.Metrics{ID: "Unknown", MType: models.Gauge}
		body, _ := json.Marshal(m)
		req := httptest.NewRequest(http.MethodPost, "/value", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
		}
	})

	t.Run("empty body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/value", bytes.NewReader([]byte{}))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})
}
