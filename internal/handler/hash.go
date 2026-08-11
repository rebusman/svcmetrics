package handler

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/rebusman/svcmetrics/internal/hashing"
)

// maxSignedBodySize caps the request body the signature check is willing to
// hold. Verification needs the whole body at once, so without a cap a single
// request could pin an arbitrary amount of memory — the handlers below decode
// their bodies as a stream and never had to.
const maxSignedBodySize = 10 << 20 // 10 MiB

// hashResponseWriter buffers a response so its complete body can be signed
// before the headers are sent to the client. Buffering is the point, so it
// deliberately implements neither [http.Flusher] nor [http.Hijacker]: a
// response that has to reach the client in pieces cannot be signed as a whole,
// and a hijacked connection leaves the middleware nothing to sign.
type hashResponseWriter struct {
	header      http.Header
	body        bytes.Buffer
	status      int
	wroteHeader bool
}

func newHashResponseWriter() *hashResponseWriter {
	return &hashResponseWriter{header: make(http.Header), status: http.StatusOK}
}

func (w *hashResponseWriter) Header() http.Header { return w.header }

func (w *hashResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
}

func (w *hashResponseWriter) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.body.Write(p)
}

// bodyAllowed reports whether a response with this status may carry a body, and
// so whether it may carry a Content-Length.
func bodyAllowed(status int) bool {
	if status >= 100 && status < 200 {
		return false
	}
	return status != http.StatusNoContent && status != http.StatusNotModified
}

// signatureAccepted reports whether a request carrying sig may proceed. An
// absent signature — or the [hashing.NoSignature] placeholder sent in its
// place — means the client has no key, and the body is taken as it is: a key
// enables verification for the clients that sign, it does not turn a signature
// into an authentication requirement for every request. A signature that is
// present has to match.
func signatureAccepted(sig string, body []byte, key string) bool {
	if sig == "" || sig == hashing.NoSignature {
		return true
	}
	return hashing.Equal(sig, body, key)
}

// HashMiddleware returns middleware that authenticates request and response
// bodies with HMAC-SHA256 using key. An empty key disables both verification
// and signing and returns the supplied handler unchanged.
//
// For a non-empty key, the middleware reads the request body as transmitted,
// before decompression, and compares its digest with [hashing.Header] when the
// client sent one. A mismatch prevents the next handler from running and
// produces a [http.StatusBadRequest] response; a body above
// [maxSignedBodySize] produces [http.StatusRequestEntityTooLarge]. A request
// without a signature passes through untouched — see [signatureAccepted]. The
// request body is restored afterwards so downstream handlers read it normally.
//
// Responses are buffered in full and signed after downstream middleware has
// finished, so [hashing.Header] describes the exact bytes sent to the client,
// including compression when enabled. This mirrors the request direction, where
// the digest also covers the compressed body. Rejection responses are signed as
// well.
//
// Place this middleware outside the compression middleware and inside the
// logging one: the first keeps the digest over the transmitted bytes, the
// second keeps rejected requests in the log.
func HashMiddleware(key string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if key == "" {
			return next
		}

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rw := newHashResponseWriter()

			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSignedBodySize))
			var tooLarge *http.MaxBytesError
			switch {
			case errors.As(err, &tooLarge):
				http.Error(rw, "Request body too large", http.StatusRequestEntityTooLarge)
			case err != nil:
				http.Error(rw, "Failed to read request body", http.StatusBadRequest)
			case !signatureAccepted(r.Header.Get(hashing.Header), body, key):
				http.Error(rw, "Invalid request hash", http.StatusBadRequest)
			default:
				r.Body = io.NopCloser(bytes.NewReader(body))
				next.ServeHTTP(rw, r)
			}

			for name, values := range rw.header {
				w.Header()[name] = append([]string(nil), values...)
			}
			responseBody := rw.body.Bytes()
			w.Header().Set(hashing.Header, hashing.Sum(responseBody, key))
			if bodyAllowed(rw.status) {
				w.Header().Set("Content-Length", strconv.Itoa(len(responseBody)))
			} else {
				w.Header().Del("Content-Length")
			}
			w.WriteHeader(rw.status)
			_, _ = w.Write(responseBody)
		})
	}
}
