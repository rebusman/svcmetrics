package handler

import (
	"bytes"
	"crypto/rsa"
	"errors"
	"io"
	"net/http"

	payloadcrypto "github.com/rebusman/svcmetrics/internal/crypto"
)

// DecryptMiddleware returns middleware that decrypts request bodies marked
// with [payloadcrypto.Header] using key. Requests without this header pass through.
// A nil key rejects encrypted requests while still allowing ordinary requests.
//
// Unsupported encryption, unreadable bodies, and decryption failures produce
// [http.StatusBadRequest]. Encrypted bodies larger than 10 MiB produce
// [http.StatusRequestEntityTooLarge]. Responses are not encrypted.
//
// Place this middleware after [HashMiddleware] and before
// [GzipRequestMiddleware] so signatures cover the transmitted ciphertext and
// decompression reads the decrypted payload.
func DecryptMiddleware(key *rsa.PrivateKey) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			algorithm := r.Header.Get(payloadcrypto.Header)
			if algorithm == "" {
				next.ServeHTTP(w, r)
				return
			}
			if key == nil || algorithm != payloadcrypto.Algorithm {
				http.Error(w, "Unsupported request encryption", http.StatusBadRequest)
				return
			}
			data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSignedBodySize))
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				http.Error(w, "Request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			if err != nil {
				http.Error(w, "Failed to read request body", http.StatusBadRequest)
				return
			}
			data, err = payloadcrypto.Decrypt(key, data)
			if err != nil {
				http.Error(w, "Invalid encrypted payload", http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(data))
			r.ContentLength = int64(len(data))
			r.Header.Del(payloadcrypto.Header)
			r.Header.Del("Content-Length")
			next.ServeHTTP(w, r)
		})
	}
}
