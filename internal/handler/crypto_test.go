package handler

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"testing"

	payloadcrypto "github.com/rebusman/svcmetrics/internal/crypto"
)

func TestDecryptMiddleware(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := payloadcrypto.Encrypt(&key.PublicKey, []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Clone(encrypted)
	tampered[len(tampered)-1] ^= 1
	for _, tc := range []struct {
		name, algorithm string
		body            []byte
		status          int
	}{
		{"encrypted", payloadcrypto.Algorithm, encrypted, http.StatusNoContent},
		{"plaintext", "", []byte("payload"), http.StatusNoContent},
		{"tampered", payloadcrypto.Algorithm, tampered, http.StatusBadRequest},
		{"truncated", payloadcrypto.Algorithm, []byte("bad"), http.StatusBadRequest},
		{"unknown algorithm", "unknown", encrypted, http.StatusBadRequest},
		{"too large", payloadcrypto.Algorithm, make([]byte, maxSignedBodySize+1), http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				data := new(bytes.Buffer)
				if _, err := data.ReadFrom(r.Body); err != nil {
					t.Fatal(err)
				}
				if data.String() != "payload" {
					t.Errorf("payload = %q", data.String())
				}
				w.WriteHeader(http.StatusNoContent)
			})
			req := httptest.NewRequest(http.MethodPost, "/updates/", bytes.NewReader(tc.body))
			req.Header.Set(payloadcrypto.Header, tc.algorithm)
			response := httptest.NewRecorder()
			DecryptMiddleware(key)(next).ServeHTTP(response, req)
			if response.Code != tc.status {
				t.Fatalf("status = %d, want %d", response.Code, tc.status)
			}
			if called != (tc.status == http.StatusNoContent) {
				t.Fatal("unexpected downstream handler call")
			}
		})
	}
}
