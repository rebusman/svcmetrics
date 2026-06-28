package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rebusman/svcmetrics/internal/storage"
)

func newTestServer(s Storage) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /update/{type}/{name}/{value}", UpdateHandler(s))
	return mux
}

func TestUpdateHandlerGauge(t *testing.T) {
	s := storage.NewMemStorage()
	mux := newTestServer(s)

	req := httptest.NewRequest(http.MethodPost, "/update/gauge/Alloc/12.5", nil)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

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
	mux := newTestServer(s)

	req := httptest.NewRequest(http.MethodPost, "/update/counter/PollCount/3", nil)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	got, err := s.GetCounter("PollCount")
	if err != nil {
		t.Fatalf("GetCounter error = %v", err)
	}
	if got != 6 {
		t.Fatalf("counter value = %d, want 6", got)
	}
}

func TestUpdateHandlerErrors(t *testing.T) {
	s := storage.NewMemStorage()
	mux := newTestServer(s)

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{name: "wrong method", method: http.MethodGet, path: "/update/gauge/Alloc/1", wantStatus: http.StatusMethodNotAllowed},
		{name: "wrong type", method: http.MethodPost, path: "/update/unknown/Alloc/1", wantStatus: http.StatusBadRequest},
		{name: "bad gauge", method: http.MethodPost, path: "/update/gauge/Alloc/not-a-number", wantStatus: http.StatusBadRequest},
		{name: "bad counter", method: http.MethodPost, path: "/update/counter/PollCount/not-a-number", wantStatus: http.StatusBadRequest},
		{name: "missing value", method: http.MethodPost, path: "/update/gauge/Alloc", wantStatus: http.StatusNotFound},
		{name: "missing name and value", method: http.MethodPost, path: "/update/gauge", wantStatus: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			rec := httptest.NewRecorder()

			mux.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}
