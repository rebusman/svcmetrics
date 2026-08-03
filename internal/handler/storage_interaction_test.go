package handler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/mock/gomock"

	"github.com/rebusman/svcmetrics/internal/mocks"
	models "github.com/rebusman/svcmetrics/internal/model"
)

// These tests pin down how the handlers talk to the storage — which method is
// called, with what arguments and how its error becomes a status code. They use
// a mock instead of a real database, so they run in the default test pass; the
// SQL behind the same contract is covered by the tests behind the integration
// build tag.

// newMockRouter wires the routes under test to a mock storage.
func newMockRouter(t *testing.T) (*mocks.MockStorage, http.Handler) {
	t.Helper()

	s := mocks.NewMockStorage(gomock.NewController(t))
	return s, newTestRouter(s)
}

// TestUpdatesJSONHandlerPassesBatchToStorage verifies that the batch reaches the
// storage once, unchanged and in the order it was received.
func TestUpdatesJSONHandlerPassesBatchToStorage(t *testing.T) {
	s, r := newMockRouter(t)

	body := `[{"id":"Alloc","type":"gauge","value":1.5},{"id":"PollCount","type":"counter","delta":4}]`

	s.EXPECT().UpdateBatch(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, got []models.Metrics) error {
			if len(got) != 2 {
				return fmt.Errorf("batch has %d metrics, want 2", len(got))
			}
			if got[0].ID != "Alloc" || got[0].MType != models.Gauge || got[0].Value == nil || *got[0].Value != 1.5 {
				return fmt.Errorf("first metric = %+v, want the gauge Alloc = 1.5", got[0])
			}
			if got[1].ID != "PollCount" || got[1].MType != models.Counter || got[1].Delta == nil || *got[1].Delta != 4 {
				return fmt.Errorf("second metric = %+v, want the counter PollCount = 4", got[1])
			}
			return nil
		}).
		Times(1)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/updates/", strings.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body = %q)", rec.Code, rec.Body.String())
	}
}

// TestUpdatesJSONHandlerSkipsStorageForEmptyBatch verifies that an empty batch
// never reaches the storage: there is nothing to write, and a database round
// trip would be wasted.
func TestUpdatesJSONHandlerSkipsStorageForEmptyBatch(t *testing.T) {
	s, r := newMockRouter(t)

	s.EXPECT().UpdateBatch(gomock.Any(), gomock.Any()).Times(0)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/updates/", strings.NewReader("[]")))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// TestUpdatesJSONHandlerSkipsStorageOnMalformedBody verifies that a request the
// handler cannot decode is rejected before the storage is touched.
func TestUpdatesJSONHandlerSkipsStorageOnMalformedBody(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "malformed json", body: "{not json"},
		{name: "empty body", body: ""},
		{name: "not an array", body: `{"id":"Alloc","type":"gauge","value":1}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, r := newMockRouter(t)
			s.EXPECT().UpdateBatch(gomock.Any(), gomock.Any()).Times(0)

			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/updates/", strings.NewReader(tt.body)))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
		})
	}
}

// TestUpdatesJSONHandlerMapsStorageErrors verifies the translation of storage
// failures into status codes.
func TestUpdatesJSONHandlerMapsStorageErrors(t *testing.T) {
	tests := []struct {
		name       string
		storageErr error
		wantStatus int
	}{
		{name: "invalid metric", storageErr: fmt.Errorf("batch: %w", models.ErrInvalidMetric), wantStatus: http.StatusBadRequest},
		{name: "not found", storageErr: fmt.Errorf("batch: %w", models.ErrNotFound), wantStatus: http.StatusNotFound},
		{name: "other failure", storageErr: errors.New("connection refused"), wantStatus: http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, r := newMockRouter(t)
			s.EXPECT().UpdateBatch(gomock.Any(), gomock.Any()).Return(tt.storageErr).Times(1)

			body := `[{"id":"Alloc","type":"gauge","value":1.5}]`
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/updates/", strings.NewReader(body)))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body = %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}

// TestUpdateJSONHandlerAnswersWithTheStoredValue verifies that the response
// carries what the storage returned — the accumulated counter total, not the
// delta the client sent.
func TestUpdateJSONHandlerAnswersWithTheStoredValue(t *testing.T) {
	t.Run("gauge", func(t *testing.T) {
		s, r := newMockRouter(t)
		s.EXPECT().UpdateGauge(gomock.Any(), "Alloc", 1.5).Return(1.5, nil).Times(1)

		rec := httptest.NewRecorder()
		body := `{"id":"Alloc","type":"gauge","value":1.5}`
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/update", strings.NewReader(body)))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("Content-Type = %s, want application/json", ct)
		}
		if got := strings.TrimSpace(rec.Body.String()); got != `{"id":"Alloc","type":"gauge","value":1.5}` {
			t.Errorf("body = %s", got)
		}
	})

	t.Run("counter", func(t *testing.T) {
		s, r := newMockRouter(t)
		s.EXPECT().UpdateCounter(gomock.Any(), "PollCount", int64(4)).Return(int64(11), nil).Times(1)

		rec := httptest.NewRecorder()
		body := `{"id":"PollCount","type":"counter","delta":4}`
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/update", strings.NewReader(body)))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if got := strings.TrimSpace(rec.Body.String()); got != `{"id":"PollCount","type":"counter","delta":11}` {
			t.Errorf("body = %s, want the running total from the storage", got)
		}
	})
}

// TestUpdateHandlerPassesParsedPathValues verifies that the plain endpoint
// parses the path and hands typed values to the storage.
func TestUpdateHandlerPassesParsedPathValues(t *testing.T) {
	t.Run("gauge", func(t *testing.T) {
		s, r := newMockRouter(t)
		s.EXPECT().UpdateGauge(gomock.Any(), "Alloc", 12.5).Return(12.5, nil).Times(1)

		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/update/gauge/Alloc/12.5", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	})

	t.Run("counter", func(t *testing.T) {
		s, r := newMockRouter(t)
		s.EXPECT().UpdateCounter(gomock.Any(), "PollCount", int64(7)).Return(int64(7), nil).Times(1)

		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/update/counter/PollCount/7", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	})

	t.Run("unparsable value never reaches the storage", func(t *testing.T) {
		s, r := newMockRouter(t)
		s.EXPECT().UpdateGauge(gomock.Any(), gomock.Any(), gomock.Any()).Times(0)

		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/update/gauge/Alloc/not-a-number", nil))

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})
}

// TestValueJSONHandlerReadsThroughStorage verifies that a lookup asks the
// storage for the requested metric and turns a missing one into 404.
func TestValueJSONHandlerReadsThroughStorage(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		s, r := newMockRouter(t)
		s.EXPECT().GetCounter(gomock.Any(), "PollCount").Return(int64(9), nil).Times(1)

		rec := httptest.NewRecorder()
		body := `{"id":"PollCount","type":"counter"}`
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/value", strings.NewReader(body)))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("Content-Type = %s, want application/json", ct)
		}
		if got := strings.TrimSpace(rec.Body.String()); got != `{"id":"PollCount","type":"counter","delta":9}` {
			t.Errorf("body = %s", got)
		}
	})

	t.Run("missing", func(t *testing.T) {
		s, r := newMockRouter(t)
		s.EXPECT().GetGauge(gomock.Any(), "Unknown").
			Return(float64(0), fmt.Errorf("gauge %q: %w", "Unknown", models.ErrNotFound)).Times(1)

		rec := httptest.NewRecorder()
		body := `{"id":"Unknown","type":"gauge"}`
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/value", strings.NewReader(body)))

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})
}

// TestListHandlerReadsBothCollections verifies that the page is rendered from
// what the storage returned, reading gauges and counters exactly once each.
func TestListHandlerReadsBothCollections(t *testing.T) {
	s, r := newMockRouter(t)

	s.EXPECT().GetAllGauges(gomock.Any()).Return(map[string]float64{"Alloc": 1.5}, nil).Times(1)
	s.EXPECT().GetAllCounters(gomock.Any()).Return(map[string]int64{"PollCount": 3}, nil).Times(1)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html" {
		t.Errorf("Content-Type = %s, want text/html", ct)
	}
	for _, want := range []string{"Alloc", "1.5", "PollCount", "3"} {
		if !bytes.Contains(rec.Body.Bytes(), []byte(want)) {
			t.Errorf("page does not mention %q:\n%s", want, rec.Body.String())
		}
	}
}
