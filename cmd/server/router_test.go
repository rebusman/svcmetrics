package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	"go.uber.org/mock/gomock"

	"github.com/rebusman/svcmetrics/internal/mocks"
	models "github.com/rebusman/svcmetrics/internal/model"
)

// These tests check the wiring — route to storage call — against a mock instead
// of a database, so they belong to the default test pass. The tests behind the
// e2e build tag cover the same paths against real PostgreSQL.

// newMockedServer builds the production router on top of a mock storage and a
// mock pinger.
func newMockedServer(t *testing.T) (*mocks.MockStorage, *mocks.MockPinger, http.Handler) {
	t.Helper()

	ctrl := gomock.NewController(t)
	storage := mocks.NewMockStorage(ctrl)
	pinger := mocks.NewMockPinger(ctrl)

	log := logrus.New()
	log.SetOutput(io.Discard)

	return storage, pinger, newRouter(log, storage, pinger)
}

// TestRouterSendsBatchToStorage verifies that both spellings of the batch path
// reach UpdateBatch with the decoded metrics.
func TestRouterSendsBatchToStorage(t *testing.T) {
	for _, path := range []string{"/updates/", "/updates"} {
		t.Run(path, func(t *testing.T) {
			storage, _, r := newMockedServer(t)

			storage.EXPECT().UpdateBatch(gomock.Any(), gomock.Any()).
				DoAndReturn(func(_ context.Context, got []models.Metrics) error {
					if len(got) != 1 || got[0].ID != "Alloc" || got[0].Value == nil || *got[0].Value != 1.5 {
						t.Errorf("storage received %+v, want a single gauge Alloc = 1.5", got)
					}
					return nil
				}).
				Times(1)

			body := `[{"id":"Alloc","type":"gauge","value":1.5}]`
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body = %q)", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestRouterDecompressesBatchBeforeStorage verifies that a gzipped batch is
// decompressed by the middleware and reaches the storage decoded.
func TestRouterDecompressesBatchBeforeStorage(t *testing.T) {
	storage, _, r := newMockedServer(t)

	storage.EXPECT().UpdateBatch(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, got []models.Metrics) error {
			if len(got) != 2 {
				t.Errorf("storage received %d metrics, want 2", len(got))
			}
			return nil
		}).
		Times(1)

	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	payload := `[{"id":"Alloc","type":"gauge","value":1.5},{"id":"PollCount","type":"counter","delta":2}]`
	if _, err := io.WriteString(zw, payload); err != nil {
		t.Fatalf("gzip write error = %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/updates/", bytes.NewReader(compressed.Bytes()))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body = %q)", rec.Code, rec.Body.String())
	}
}

// TestRouterKeepsSingleMetricEndpoints verifies that the older endpoints still
// route to their storage methods next to the batch one.
func TestRouterKeepsSingleMetricEndpoints(t *testing.T) {
	storage, _, r := newMockedServer(t)

	storage.EXPECT().UpdateGauge(gomock.Any(), "Alloc", 12.5).Return(12.5, nil).Times(1)
	storage.EXPECT().UpdateCounter(gomock.Any(), "PollCount", int64(3)).Return(int64(3), nil).Times(1)
	storage.EXPECT().GetGauge(gomock.Any(), "Alloc").Return(12.5, nil).Times(1)

	requests := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/update/gauge/Alloc/12.5", ""},
		{http.MethodPost, "/update", `{"id":"PollCount","type":"counter","delta":3}`},
		{http.MethodGet, "/value/gauge/Alloc", ""},
	}

	for _, req := range requests {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(req.method, req.path, strings.NewReader(req.body)))

		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s: status = %d, want 200 (body = %q)", req.method, req.path, rec.Code, rec.Body.String())
		}
	}
}

// TestRouterPingUsesPinger verifies that /ping asks the database rather than
// answering on its own.
func TestRouterPingUsesPinger(t *testing.T) {
	_, pinger, r := newMockedServer(t)

	pinger.EXPECT().PingContext(gomock.Any()).Return(nil).Times(1)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ping", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
