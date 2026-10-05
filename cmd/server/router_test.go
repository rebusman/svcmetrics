package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"go.uber.org/mock/gomock"

	"github.com/rebusman/svcmetrics/internal/hashing"
	"github.com/rebusman/svcmetrics/internal/mocks"
	models "github.com/rebusman/svcmetrics/internal/model"
)

// These tests check the wiring — route to storage call — against a mock instead
// of a database, so they belong to the default test pass. The tests behind the
// e2e build tag cover the same paths against real PostgreSQL.

// newMockedServer builds the production router on top of a mock storage and a
// mock pinger, without request signing.
func newMockedServer(t *testing.T) (*mocks.MockStorage, *mocks.MockPinger, http.Handler) {
	t.Helper()

	storage, pinger, r, _ := newMockedServerWithKey(t, "")
	return storage, pinger, r
}

// newMockedServerWithKey builds the same router with signing enabled and hands
// back the buffer the request log is written to, so tests can assert on what
// the server recorded about a request.
func newMockedServerWithKey(t *testing.T, key string) (*mocks.MockStorage, *mocks.MockPinger, http.Handler, *bytes.Buffer) {
	t.Helper()

	ctrl := gomock.NewController(t)
	storage := mocks.NewMockStorage(ctrl)
	pinger := mocks.NewMockPinger(ctrl)

	log := logrus.New()
	var logged bytes.Buffer
	log.SetOutput(&logged)

	return storage, pinger, newRouter(log, storage, pinger, key, nil), &logged
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

// TestRouterBoundsTheRequestTime verifies that the storage is called with a
// context that already carries the request deadline, which is what lets a call
// hanging on the database end with an answer instead of holding the connection.
func TestRouterBoundsTheRequestTime(t *testing.T) {
	storage, _, r := newMockedServer(t)

	storage.EXPECT().UpdateBatch(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, _ []models.Metrics) error {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("the storage was called with a context without a deadline")
			}
			if left := time.Until(deadline); left <= 0 || left > requestTimeout {
				t.Errorf("deadline is %s away, want at most %s", left, requestTimeout)
			}
			return nil
		}).
		Times(1)

	body := `[{"id":"Alloc","type":"gauge","value":1.5}]`
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/updates/", strings.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body = %q)", rec.Code, rec.Body.String())
	}
}

// TestRouterAnswersGatewayTimeoutOnAnExpiredDeadline verifies the status a
// request gets when it runs past the deadline the router set: the storage sees
// a cancelled context and its error becomes 504 rather than 500.
func TestRouterAnswersGatewayTimeoutOnAnExpiredDeadline(t *testing.T) {
	storage, _, r := newMockedServer(t)

	storage.EXPECT().UpdateBatch(gomock.Any(), gomock.Any()).
		Return(fmt.Errorf("batch: %w", context.DeadlineExceeded)).
		Times(1)

	body := `[{"id":"Alloc","type":"gauge","value":1.5}]`
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/updates/", strings.NewReader(body)))

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want %d (body = %q)", rec.Code, http.StatusGatewayTimeout, rec.Body.String())
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

// TestRouterVerifiesSignedBatch checks the signature middleware in its place in
// the chain: the digest is taken over the compressed body the client sent, and
// the response is signed over the compressed body the client receives.
func TestRouterVerifiesSignedBatch(t *testing.T) {
	const key = "secret"

	storage, _, r, _ := newMockedServerWithKey(t, key)
	storage.EXPECT().UpdateBatch(gomock.Any(), gomock.Any()).Return(nil).Times(1)

	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	payload := `[{"id":"Alloc","type":"gauge","value":1.5}]`
	if _, err := io.WriteString(zw, payload); err != nil {
		t.Fatalf("gzip write error = %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close error = %v", err)
	}
	body := compressed.Bytes()

	req := httptest.NewRequest(http.MethodPost, "/updates/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set(hashing.Header, hashing.Sum(body, key))

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body = %q)", rec.Code, rec.Body.String())
	}
	if !hashing.Equal(rec.Header().Get(hashing.Header), rec.Body.Bytes(), key) {
		t.Error("response signature does not match the transmitted body")
	}
}

// TestRouterServesUnsignedReads checks that a key does not lock out the clients
// that never sign: the HTML page, /value and /ping are read with plain requests
// and have to keep answering.
func TestRouterServesUnsignedReads(t *testing.T) {
	const key = "secret"

	storage, pinger, r, _ := newMockedServerWithKey(t, key)
	storage.EXPECT().GetGauge(gomock.Any(), "Alloc").Return(12.5, nil).Times(1)
	storage.EXPECT().GetAllGauges(gomock.Any()).Return(map[string]float64{"Alloc": 12.5}, nil).Times(1)
	storage.EXPECT().GetAllCounters(gomock.Any()).Return(map[string]int64{}, nil).Times(1)
	pinger.EXPECT().PingContext(gomock.Any()).Return(nil).Times(1)

	for _, path := range []string{"/value/gauge/Alloc", "/", "/ping"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body = %q)", rec.Code, rec.Body.String())
			}
			if !hashing.Equal(rec.Header().Get(hashing.Header), rec.Body.Bytes(), key) {
				t.Error("response signature does not match the transmitted body")
			}
		})
	}
}

// TestRouterRejectsAndLogsBadSignature checks that a forged request never
// reaches the storage and still shows up in the request log, which is why the
// signature middleware sits inside the logging one.
func TestRouterRejectsAndLogsBadSignature(t *testing.T) {
	const key = "secret"

	_, _, r, logged := newMockedServerWithKey(t, key)

	body := `[{"id":"Alloc","type":"gauge","value":1.5}]`
	req := httptest.NewRequest(http.MethodPost, "/updates/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(hashing.Header, hashing.Sum([]byte(body), "another key"))

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body = %q)", rec.Code, rec.Body.String())
	}
	if entry := logged.String(); !strings.Contains(entry, "/updates/") || !strings.Contains(entry, "400") {
		t.Errorf("request log = %q, want the rejected request in it", entry)
	}
}
