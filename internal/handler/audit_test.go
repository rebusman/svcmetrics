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

	"github.com/rebusman/svcmetrics/internal/audit"
	"github.com/rebusman/svcmetrics/internal/repository"
)

// auditSpy is an Auditor that remembers the events it was given.
type auditSpy struct {
	events []audit.Event
}

func (a *auditSpy) Notify(_ context.Context, e audit.Event) {
	a.events = append(a.events, e)
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
			before := time.Now().Unix()
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body = %q)", rec.Code, rec.Body.String())
			}
			if len(spy.events) != 1 {
				t.Fatalf("got %d events, want 1", len(spy.events))
			}
			e := spy.events[0]
			if !reflect.DeepEqual(e.Metrics, tt.metrics) {
				t.Errorf("metrics = %q, want %q", e.Metrics, tt.metrics)
			}
			if e.IPAddress != "192.168.0.42" {
				t.Errorf("ip_address = %q, want 192.168.0.42", e.IPAddress)
			}
			if e.TS < before || e.TS > time.Now().Unix() {
				t.Errorf("ts = %d, not the time of the request", e.TS)
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
