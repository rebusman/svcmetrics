package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	models "github.com/rebusman/svcmetrics/internal/model"
	"github.com/rebusman/svcmetrics/internal/repository"
)

func TestValueHandler(t *testing.T) {
	ctx := context.Background()
	s := repository.NewMemStorage()
	_, _ = s.UpdateGauge(ctx, "Alloc", 12.5)
	_, _ = s.UpdateCounter(ctx, "PollCount", 5)
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

// TestValueHandlerFormatsGauges verifies that gauges are rendered without a
// trailing exponent or padding, so a client can parse back exactly what the
// plain text endpoint returned.
func TestValueHandlerFormatsGauges(t *testing.T) {
	ctx := context.Background()
	s := repository.NewMemStorage()
	r := newTestRouter(s)

	tests := []struct {
		name  string
		value float64
		want  string
	}{
		{"integral", 42, "42"},
		{"fractional", 0.15625, "0.15625"},
		{"negative", -2.5, "-2.5"},
		{"zero", 0, "0"},
		{"large", 1e21, "1000000000000000000000"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := s.UpdateGauge(ctx, tt.name, tt.value); err != nil {
				t.Fatalf("UpdateGauge error = %v", err)
			}
			req := httptest.NewRequest(http.MethodGet, "/value/gauge/"+tt.name, nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Body.String(); got != tt.want {
				t.Errorf("body = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValueJSONHandler(t *testing.T) {
	ctx := context.Background()
	s := repository.NewMemStorage()
	gaugeVal := 12.5
	_, _ = s.UpdateGauge(ctx, "Alloc", gaugeVal)
	_, _ = s.UpdateCounter(ctx, "PollCount", 7)
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

func TestValueJSONHandlerRejectsBadRequests(t *testing.T) {
	s := repository.NewMemStorage()
	r := newTestRouter(s)

	tests := []struct {
		name string
		body string
	}{
		{"malformed json", `{"id":`},
		{"not an object", `"just a string"`},
		{"missing id", `{"type":"gauge"}`},
		{"unknown type", `{"id":"Alloc","type":"histogram"}`},
		{"empty type", `{"id":"Alloc"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/value", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 (body = %q)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestListHandler(t *testing.T) {
	ctx := context.Background()
	s := repository.NewMemStorage()
	_, _ = s.UpdateGauge(ctx, "Alloc", 12.5)
	_, _ = s.UpdateCounter(ctx, "PollCount", 3)
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

	body := rec.Body.String()
	for _, want := range []string{"Alloc", "12.5", "PollCount", "3"} {
		if !strings.Contains(body, want) {
			t.Errorf("body does not mention %q:\n%s", want, body)
		}
	}
}

func TestListHandlerOnEmptyStorage(t *testing.T) {
	r := newTestRouter(repository.NewMemStorage())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<html>") {
		t.Errorf("body is not a page:\n%s", rec.Body.String())
	}
}

// failingWriter accepts the status line but fails every body write, like a
// client that hung up once the response started.
type failingWriter struct {
	hdr           http.Header
	status        int
	headerWrites  int
	attempted     bytes.Buffer
	writeAttempts int
}

func (f *failingWriter) Header() http.Header {
	if f.hdr == nil {
		f.hdr = make(http.Header)
	}
	return f.hdr
}

func (f *failingWriter) WriteHeader(code int) {
	f.headerWrites++
	if f.headerWrites == 1 {
		f.status = code
	}
}

func (f *failingWriter) Write(p []byte) (int, error) {
	f.writeAttempts++
	f.attempted.Write(p)
	return 0, errors.New("connection reset by peer")
}

// TestJSONHandlersDoNotAppendErrorAfterCommitting verifies that a failed body
// write does not append an error message on top of the JSON: once the 200 is
// committed the handler cannot take it back.
func TestJSONHandlersDoNotAppendErrorAfterCommitting(t *testing.T) {
	ctx := context.Background()
	s := repository.NewMemStorage()
	_, _ = s.UpdateGauge(ctx, "Alloc", 12.5)
	r := newTestRouter(s)

	tests := []struct {
		name string
		path string
		body string
	}{
		{"value", "/value", `{"id":"Alloc","type":"gauge"}`},
		{"update", "/update", `{"id":"Alloc","type":"gauge","value":3.5}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &failingWriter{}
			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)

			if w.status != http.StatusOK {
				t.Fatalf("status = %d, want 200", w.status)
			}
			if w.headerWrites != 1 {
				t.Errorf("WriteHeader called %d times, want 1 (a second call cannot change the response)", w.headerWrites)
			}
			if got := w.attempted.String(); strings.Contains(got, "Internal Server Error") {
				t.Errorf("handler appended an error to the committed body: %q", got)
			}
			if w.writeAttempts != 1 {
				t.Errorf("body written in %d calls, want 1", w.writeAttempts)
			}
		})
	}
}

// errStorage fails every operation with a plain error — not ErrNotFound — so
// the handlers must answer 500 rather than 404.
type errStorage struct{ err error }

func (e errStorage) GetGauge(context.Context, string) (float64, error) { return 0, e.err }
func (e errStorage) GetCounter(context.Context, string) (int64, error) { return 0, e.err }
func (e errStorage) GetAllGauges(context.Context) (map[string]float64, error) {
	return nil, e.err
}
func (e errStorage) GetAllCounters(context.Context) (map[string]int64, error) {
	return nil, e.err
}
func (e errStorage) UpdateGauge(context.Context, string, float64) (float64, error) {
	return 0, e.err
}
func (e errStorage) UpdateCounter(context.Context, string, int64) (int64, error) {
	return 0, e.err
}
func (e errStorage) UpdateBatch(context.Context, []models.Metrics) error { return e.err }

func TestReadHandlersReportStorageFailuresAs500(t *testing.T) {
	s := errStorage{err: errors.New("database is on fire")}
	r := newTestRouter(s)

	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"plain gauge", http.MethodGet, "/value/gauge/Alloc", ""},
		{"plain counter", http.MethodGet, "/value/counter/PollCount", ""},
		{"json gauge", http.MethodPost, "/value", `{"id":"Alloc","type":"gauge"}`},
		{"json counter", http.MethodPost, "/value", `{"id":"PollCount","type":"counter"}`},
		{"list", http.MethodGet, "/", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500 (body = %q)", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "database is on fire") {
				t.Errorf("body = %q, want it to carry the storage error", rec.Body.String())
			}
		})
	}
}

// TestListHandlerReportsCounterFailure verifies that a failure of the counter
// read, which happens after the gauge read, surfaces instead of rendering a
// half-filled page.
func TestListHandlerReportsCounterFailure(t *testing.T) {
	r := newTestRouter(countersFailStorage{err: errors.New("counters unavailable")})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body = %q)", rec.Code, rec.Body.String())
	}
}

type countersFailStorage struct {
	errStorage
	err error
}

func (c countersFailStorage) GetAllGauges(context.Context) (map[string]float64, error) {
	return map[string]float64{"Alloc": 1}, nil
}

func (c countersFailStorage) GetAllCounters(context.Context) (map[string]int64, error) {
	return nil, c.err
}
