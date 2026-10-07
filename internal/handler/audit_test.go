package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rebusman/svcmetrics/internal/repository"
)

// auditCall is one call to [auditSpy.Notify].
type auditCall struct {
	time    time.Time
	metrics []string
	ip      string
}

// auditSpy is an Auditor that remembers the calls it was given.
type auditSpy struct {
	events []auditCall
}

func (a *auditSpy) Notify(_ context.Context, t time.Time, metrics []string, ip string) {
	a.events = append(a.events, auditCall{time: t, metrics: metrics, ip: ip})
}

func newAuditedRouter(a Auditor) chi.Router {
	s := repository.NewMemStorage()
	r := chi.NewRouter()
	r.Post("/update", UpdateJSONHandler(s, a))
	r.Post("/updates/", UpdatesJSONHandler(s, a))
	r.Post("/update/{type}/{name}/{value}", UpdateHandler(s, a))
	return r
}

func TestUpdateHandlersNotifyAuditor(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		body    string
		metrics []string
	}{
		{"json", "/update", `{"id":"Alloc","type":"gauge","value":1.5}`, []string{"Alloc"}},
		{"batch", "/updates/", `[{"id":"Alloc","type":"gauge","value":1.5},{"id":"PollCount","type":"counter","delta":2}]`, []string{"Alloc", "PollCount"}},
		{"path", "/update/counter/PollCount/3", "", []string{"PollCount"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spy := &auditSpy{}
			r := newAuditedRouter(spy)

			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			req.RemoteAddr = "192.168.0.42:51234"
			before := time.Now()
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body = %q)", rec.Code, rec.Body.String())
			}
			if len(spy.events) != 1 {
				t.Fatalf("got %d events, want 1", len(spy.events))
			}
			e := spy.events[0]
			if !reflect.DeepEqual(e.metrics, tt.metrics) {
				t.Errorf("metrics = %q, want %q", e.metrics, tt.metrics)
			}
			if e.ip != "192.168.0.42" {
				t.Errorf("ip = %q, want 192.168.0.42", e.ip)
			}
			if e.time.Before(before) || e.time.After(time.Now()) {
				t.Errorf("time = %v, not the time of the request", e.time)
			}
		})
	}
}

func TestUpdateHandlersSkipAuditOnFailure(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
	}{
		{"invalid json", "/update", `{"id":"Alloc","type":"unknown","value":1}`},
		{"empty batch", "/updates/", `[]`},
		{"invalid batch", "/updates/", `[{"id":"Alloc","type":"gauge"}]`},
		{"invalid path value", "/update/gauge/Alloc/abc", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spy := &auditSpy{}
			r := newAuditedRouter(spy)

			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body)))

			if len(spy.events) != 0 {
				t.Errorf("got events %+v for a request that stored nothing", spy.events)
			}
		})
	}
}
