package handler

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/mock/gomock"

	"github.com/rebusman/svcmetrics/internal/mocks"
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

func TestValueJSONHandlerRejectsBadRequests(t *testing.T) {
	s := repository.NewMemStorage()
	r := newTestRouter(s)

	tests := []struct {
		name string
		body string
	}{
		{"empty body", ""},
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

// newFailingStorage returns a mock storage whose every read fails with err —
// a plain error, not ErrNotFound — so the handlers must answer 500, not 404.
func newFailingStorage(t *testing.T, err error) *mocks.MockStorage {
	t.Helper()

	s := mocks.NewMockStorage(gomock.NewController(t))
	s.EXPECT().GetGauge(gomock.Any(), gomock.Any()).Return(float64(0), err).AnyTimes()
	s.EXPECT().GetCounter(gomock.Any(), gomock.Any()).Return(int64(0), err).AnyTimes()
	s.EXPECT().GetAllGauges(gomock.Any()).Return(nil, err).AnyTimes()
	s.EXPECT().GetAllCounters(gomock.Any()).Return(nil, err).AnyTimes()
	return s
}

func TestReadHandlersReportStorageFailuresAs500(t *testing.T) {
	s := newFailingStorage(t, errors.New("database is on fire"))
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
	ctrl := gomock.NewController(t)
	s := mocks.NewMockStorage(ctrl)

	gomock.InOrder(
		s.EXPECT().GetAllGauges(gomock.Any()).Return(map[string]float64{"Alloc": 1}, nil),
		s.EXPECT().GetAllCounters(gomock.Any()).Return(nil, errors.New("counters unavailable")),
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	newTestRouter(s).ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body = %q)", rec.Code, rec.Body.String())
	}
}
