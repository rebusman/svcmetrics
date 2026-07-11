package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/rebusman/svcmetrics/internal/storage"
)

func newTestRouter(s Storage) chi.Router {
	r := chi.NewRouter()
	r.Post("/update/{type}/{name}/{value}", UpdateHandler(s))
	r.Get("/value/{type}/{name}", ValueHandler(s))
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
		{"invalid type", "/value/unknown/Alloc", http.StatusNotFound, ""},
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
