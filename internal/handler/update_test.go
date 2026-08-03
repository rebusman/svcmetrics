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
	"github.com/rebusman/svcmetrics/internal/repository"
)

func newTestRouter(s repository.Storage) chi.Router {
	r := chi.NewRouter()
	r.Post("/update", UpdateJSONHandler(s))
	r.Post("/updates", UpdatesJSONHandler(s))
	r.Post("/updates/", UpdatesJSONHandler(s))
	r.Post("/update/{type}/{name}/{value}", UpdateHandler(s))
	r.Get("/value/{type}/{name}", ValueHandler(s))
	r.Post("/value", ValueJSONHandler(s))
	r.Get("/", ListHandler(s))
	return r
}

func TestUpdateHandlerGauge(t *testing.T) {
	s := repository.NewMemStorage()
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
	s := repository.NewMemStorage()
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
	s := repository.NewMemStorage()
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

func TestUpdatesJSONHandler(t *testing.T) {
	t.Run("stores the whole batch", func(t *testing.T) {
		s := repository.NewMemStorage()
		r := newTestRouter(s)

		alloc, sys := 1.5, 2.5
		delta := int64(3)
		batch := []models.Metrics{
			{ID: "Alloc", MType: models.Gauge, Value: &alloc},
			{ID: "Sys", MType: models.Gauge, Value: &sys},
			{ID: "PollCount", MType: models.Counter, Delta: &delta},
		}
		body, _ := json.Marshal(batch)

		req := httptest.NewRequest(http.MethodPost, "/updates/", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("Content-Type = %s, want application/json", ct)
		}

		ctx := context.Background()
		if got, err := s.GetGauge(ctx, "Alloc"); err != nil || got != 1.5 {
			t.Errorf("Alloc = %v (err %v), want 1.5", got, err)
		}
		if got, err := s.GetGauge(ctx, "Sys"); err != nil || got != 2.5 {
			t.Errorf("Sys = %v (err %v), want 2.5", got, err)
		}
		if got, err := s.GetCounter(ctx, "PollCount"); err != nil || got != 3 {
			t.Errorf("PollCount = %v (err %v), want 3", got, err)
		}
	})

	t.Run("folds duplicates", func(t *testing.T) {
		s := repository.NewMemStorage()
		r := newTestRouter(s)

		first, last := 1.0, 9.0
		d1, d2 := int64(2), int64(5)
		batch := []models.Metrics{
			{ID: "Alloc", MType: models.Gauge, Value: &first},
			{ID: "PollCount", MType: models.Counter, Delta: &d1},
			{ID: "Alloc", MType: models.Gauge, Value: &last},
			{ID: "PollCount", MType: models.Counter, Delta: &d2},
		}
		body, _ := json.Marshal(batch)

		req := httptest.NewRequest(http.MethodPost, "/updates/", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}

		ctx := context.Background()
		if got, err := s.GetGauge(ctx, "Alloc"); err != nil || got != 9.0 {
			t.Errorf("Alloc = %v (err %v), want 9", got, err)
		}
		if got, err := s.GetCounter(ctx, "PollCount"); err != nil || got != 7 {
			t.Errorf("PollCount = %v (err %v), want 7", got, err)
		}
	})

	t.Run("rejects the batch as a whole", func(t *testing.T) {
		s := repository.NewMemStorage()
		r := newTestRouter(s)

		val := 1.5
		batch := []models.Metrics{
			{ID: "Alloc", MType: models.Gauge, Value: &val},
			{ID: "Broken", MType: "histogram", Value: &val},
		}
		body, _ := json.Marshal(batch)

		req := httptest.NewRequest(http.MethodPost, "/updates/", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
		if _, err := s.GetGauge(context.Background(), "Alloc"); err == nil {
			t.Error("Alloc was stored even though the batch was rejected")
		}
	})

	t.Run("missing id", func(t *testing.T) {
		s := repository.NewMemStorage()
		r := newTestRouter(s)

		val := 1.5
		body, _ := json.Marshal([]models.Metrics{{MType: models.Gauge, Value: &val}})

		req := httptest.NewRequest(http.MethodPost, "/updates/", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("value missing for gauge", func(t *testing.T) {
		s := repository.NewMemStorage()
		r := newTestRouter(s)

		body, _ := json.Marshal([]models.Metrics{{ID: "Alloc", MType: models.Gauge}})

		req := httptest.NewRequest(http.MethodPost, "/updates/", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("empty batch", func(t *testing.T) {
		s := repository.NewMemStorage()
		r := newTestRouter(s)

		req := httptest.NewRequest(http.MethodPost, "/updates/", strings.NewReader("[]"))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
		}
	})

	t.Run("empty body", func(t *testing.T) {
		s := repository.NewMemStorage()
		r := newTestRouter(s)

		req := httptest.NewRequest(http.MethodPost, "/updates/", bytes.NewReader(nil))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		s := repository.NewMemStorage()
		r := newTestRouter(s)

		req := httptest.NewRequest(http.MethodPost, "/updates/", strings.NewReader("{not json"))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("without trailing slash", func(t *testing.T) {
		s := repository.NewMemStorage()
		r := newTestRouter(s)

		val := 4.5
		body, _ := json.Marshal([]models.Metrics{{ID: "Alloc", MType: models.Gauge, Value: &val}})

		req := httptest.NewRequest(http.MethodPost, "/updates", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if got, err := s.GetGauge(context.Background(), "Alloc"); err != nil || got != 4.5 {
			t.Errorf("Alloc = %v (err %v), want 4.5", got, err)
		}
	})

	t.Run("keeps the single-metric API working", func(t *testing.T) {
		s := repository.NewMemStorage()
		r := newTestRouter(s)

		val := 2.5
		body, _ := json.Marshal([]models.Metrics{{ID: "Alloc", MType: models.Gauge, Value: &val}})
		req := httptest.NewRequest(http.MethodPost, "/updates/", bytes.NewReader(body))
		r.ServeHTTP(httptest.NewRecorder(), req)

		req = httptest.NewRequest(http.MethodPost, "/update/gauge/Alloc/7.5", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if got, err := s.GetGauge(context.Background(), "Alloc"); err != nil || got != 7.5 {
			t.Errorf("Alloc = %v (err %v), want 7.5", got, err)
		}
	})
}
