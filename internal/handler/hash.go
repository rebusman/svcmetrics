package handler

import (
	"bytes"
	"errors"
	"io"
	"maps"
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

// unsigned reports whether sig means the client did not sign the request: an
// absent signature, or the [hashing.NoSignature] placeholder some clients send
// in its place. Such a request is taken as it is — a key enables verification
// for the clients that sign, it does not turn a signature into an
// authentication requirement for every request.
func unsigned(sig string) bool {
	return sig == "" || sig == hashing.NoSignature
}

// HashMiddleware returns middleware that authenticates request and response
// bodies with HMAC-SHA256 using key. An empty key disables both verification
// and signing and returns the supplied handler unchanged.
//
// For a non-empty key, a request carrying [hashing.Header] is read as
// transmitted, before decompression, and the digest of the body is compared
// with the header. A mismatch prevents the next handler from running and
// produces a [http.StatusBadRequest] response; a body above
// [maxSignedBodySize] produces [http.StatusRequestEntityTooLarge]. The body is
// restored afterwards so downstream handlers read it normally.
//
// A request without a signature passes through untouched — see [unsigned]. Its
// body is never read here: there is nothing to verify, so it is left as a
// stream for the handlers, which neither buffers it nor holds it against
// [maxSignedBodySize]. That cap bounds what verification has to keep in memory
// and is not a limit on request size in general.
//
// Responses are buffered in full and signed after downstream middleware has
// finished, so [hashing.Header] describes the exact bytes sent to the client,
// including compression when enabled. This mirrors the request direction, where
// the digest also covers the compressed body. Rejection responses are signed as
// well. The buffered headers move to the real [http.ResponseWriter] as they
// are, without copying the value slices: the buffer is not read again once the
// response has been written out.
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

			sig := r.Header.Get(hashing.Header)
			if unsigned(sig) {
				// Nothing to verify, so nothing to hold: the body stays the
				// stream the handlers below read for themselves, and the cap
				// that bounds a verified body does not apply to it.
				next.ServeHTTP(rw, r)
			} else {
				body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSignedBodySize))
				var tooLarge *http.MaxBytesError
				switch {
				case errors.As(err, &tooLarge):
					http.Error(rw, "Request body too large", http.StatusRequestEntityTooLarge)
				case err != nil:
					http.Error(rw, "Failed to read request body", http.StatusBadRequest)
				case !hashing.Equal(sig, body, key):
					http.Error(rw, "Invalid request hash", http.StatusBadRequest)
				default:
					r.Body = io.NopCloser(bytes.NewReader(body))
					next.ServeHTTP(rw, r)
				}
			}

			maps.Copy(w.Header(), rw.header)

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
