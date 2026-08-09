package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/mock/gomock"

	"github.com/rebusman/svcmetrics/internal/mocks"
)

// newPinger returns a mock Pinger whose PingContext answers with err.
func newPinger(t *testing.T, err error) *mocks.MockPinger {
	t.Helper()

	p := mocks.NewMockPinger(gomock.NewController(t))
	p.EXPECT().PingContext(gomock.Any()).Return(err).Times(1)
	return p
}

func TestPingHandlerOK(t *testing.T) {
	h := PingHandler(newPinger(t, nil))

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	rec := httptest.NewRecorder()

	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestPingHandlerError(t *testing.T) {
	h := PingHandler(newPinger(t, errors.New("connection refused")))

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	rec := httptest.NewRecorder()

	h(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestPingHandlerNotConfigured(t *testing.T) {
	h := PingHandler(nil)

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	rec := httptest.NewRecorder()

	h(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}
