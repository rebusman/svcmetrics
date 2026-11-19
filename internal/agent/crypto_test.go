package agent

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rebusman/svcmetrics/internal/handler"
	models "github.com/rebusman/svcmetrics/internal/model"
	"github.com/rebusman/svcmetrics/internal/repository"
)

func TestEncryptedReport(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	storage := repository.NewMemStorage()
	endpoint := handler.HashMiddleware("secret")(handler.DecryptMiddleware(key)(handler.GzipRequestMiddleware(handler.UpdatesJSONHandler(storage, nil))))
	server := httptest.NewServer(endpoint)
	defer server.Close()
	a := New(server.URL, time.Second, time.Second, 32, "secret", 1)
	a.SetPublicKey(&key.PublicKey)
	delta := int64(7)
	if err := a.sendBatch(context.Background(), []models.Metrics{{ID: "PollCount", MType: models.Counter, Delta: &delta}}); err != nil {
		t.Fatal(err)
	}
	value, err := storage.GetCounter(context.Background(), "PollCount")
	if err != nil || value != delta {
		t.Fatalf("stored counter = %d, err = %v", value, err)
	}
	// An encrypted request must fail if the server has no private key.
	withoutKey := httptest.NewServer(handler.DecryptMiddleware(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("encrypted payload reached handler") })))
	defer withoutKey.Close()
	a.updatesURL = withoutKey.URL
	if err := a.sendBatch(context.Background(), []models.Metrics{{ID: "PollCount", MType: models.Counter, Delta: &delta}}); err == nil {
		t.Fatal("server without key accepted encrypted request")
	}
}
