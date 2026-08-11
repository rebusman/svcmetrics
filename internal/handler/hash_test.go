package handler

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/rebusman/svcmetrics/internal/hashing"
)

const testKey = "secret"

// echoHandler answers with body under the given content type.
func echoHandler(contentType string, body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(body)
	}
}

func TestHashMiddlewareVerifiesRequestAndSignsResponse(t *testing.T) {
	requestBody := []byte("request body")
	responseBody := []byte("response body")

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		got, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading the restored body: %v", err)
		}
		if !bytes.Equal(got, requestBody) {
			t.Errorf("restored body = %q, want %q", got, requestBody)
		}
		echoHandler("text/plain", responseBody)(w, r)
	})

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(requestBody))
	req.Header.Set(hashing.Header, hashing.Sum(requestBody, testKey))
	rec := httptest.NewRecorder()
	HashMiddleware(testKey)(next).ServeHTTP(rec, req)

	if !called {
		t.Fatal("wrapped handler was not called for a valid hash")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get(hashing.Header); got != hashing.Sum(responseBody, testKey) {
		t.Errorf("response hash = %q, want %q", got, hashing.Sum(responseBody, testKey))
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(responseBody)) {
		t.Errorf("Content-Length = %q, want %q", got, strconv.Itoa(len(responseBody)))
	}
}

// TestHashMiddlewareAcceptsUppercaseSignature covers the digest arriving in
// upper case: the comparison happens on decoded bytes, so the case of the
// hexadecimal digits must not matter.
func TestHashMiddlewareAcceptsUppercaseSignature(t *testing.T) {
	body := []byte("request body")
	called := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set(hashing.Header, strings.ToUpper(hashing.Sum(body, testKey)))
	rec := httptest.NewRecorder()
	HashMiddleware(testKey)(next).ServeHTTP(rec, req)

	if !called {
		t.Fatal("wrapped handler was not called for an uppercase hash")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

// TestHashMiddlewarePassesUnsignedRequests covers the clients that have no key:
// a key on the server enables verification for those who sign, it does not make
// a signature mandatory. Every read endpoint of the router — the HTML page,
// /value and /ping — arrives this way.
func TestHashMiddlewarePassesUnsignedRequests(t *testing.T) {
	responseBody := []byte("<html>page</html>")

	tests := []struct {
		name   string
		header string
		set    bool
	}{
		{name: "header absent"},
		{name: "header empty", header: "", set: true},
		{name: "header none", header: hashing.NoSignature, set: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				echoHandler("text/html", responseBody)(w, r)
			})

			req := httptest.NewRequest(http.MethodGet, "/value/gauge/Alloc", nil)
			if tt.set {
				req.Header.Set(hashing.Header, tt.header)
			}
			rec := httptest.NewRecorder()
			HashMiddleware(testKey)(next).ServeHTTP(rec, req)

			if !called {
				t.Fatal("wrapped handler was not called for an unsigned request")
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			if got := rec.Header().Get(hashing.Header); got != hashing.Sum(responseBody, testKey) {
				t.Errorf("response hash = %q, want %q", got, hashing.Sum(responseBody, testKey))
			}
		})
	}
}

func TestHashMiddlewareRejectsInvalidRequest(t *testing.T) {
	tests := []struct {
		name      string
		signature string
	}{
		{name: "malformed hex", signature: "invalid"},
		{name: "wrong key", signature: hashing.Sum([]byte("body"), "another key")},
		{name: "wrong body", signature: hashing.Sum([]byte("other body"), testKey)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString("body"))
			req.Header.Set(hashing.Header, tt.signature)
			rec := httptest.NewRecorder()

			HashMiddleware(testKey)(next).ServeHTTP(rec, req)

			if called {
				t.Fatal("wrapped handler was called for an invalid hash")
			}
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
			}
			if body := rec.Body.String(); !strings.Contains(body, "Invalid request hash") {
				t.Errorf("body = %q, want it to explain the rejection", body)
			}
			if got := rec.Header().Get(hashing.Header); got != hashing.Sum(rec.Body.Bytes(), testKey) {
				t.Errorf("rejection response hash = %q, want %q", got, hashing.Sum(rec.Body.Bytes(), testKey))
			}
		})
	}
}

// TestHashMiddlewareRejectsOversizedBody covers the cap on the body the
// signature check buffers.
func TestHashMiddlewareRejectsOversizedBody(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	body := bytes.Repeat([]byte("x"), maxSignedBodySize+1)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set(hashing.Header, hashing.Sum(body, testKey))
	rec := httptest.NewRecorder()

	HashMiddleware(testKey)(next).ServeHTTP(rec, req)

	if called {
		t.Fatal("wrapped handler was called for an oversized body")
	}
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
}

// TestHashMiddlewareDisabledWithoutKey covers the empty key: the handler has to
// come back untouched, with no verification and no signature.
func TestHashMiddlewareDisabledWithoutKey(t *testing.T) {
	next := echoHandler("text/plain", []byte("response body"))
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString("body"))
	req.Header.Set(hashing.Header, "invalid")
	rec := httptest.NewRecorder()

	HashMiddleware("")(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get(hashing.Header); got != "" {
		t.Errorf("response hash = %q, want no signature", got)
	}
}

// TestHashMiddlewareSignsCompressedBodies covers the middleware in the position
// it holds in the router — outside the compression pair — where both digests
// have to describe the bytes on the wire rather than the decompressed ones.
func TestHashMiddlewareSignsCompressedBodies(t *testing.T) {
	plain := []byte(`{"id":"Alloc","type":"gauge","value":1.5}`)

	var compressed bytes.Buffer
	gw := gzip.NewWriter(&compressed)
	if _, err := gw.Write(plain); err != nil {
		t.Fatalf("compressing the request body: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("closing the compressor: %v", err)
	}
	requestBody := compressed.Bytes()

	responseBody := []byte(`{"status":"ok"}`)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading the decompressed body: %v", err)
		}
		if !bytes.Equal(got, plain) {
			t.Errorf("decompressed body = %q, want %q", got, plain)
		}
		echoHandler("application/json", responseBody)(w, r)
	})

	// The same order as in the router: hash, then request and response gzip.
	handler := HashMiddleware(testKey)(GzipRequestMiddleware(GzipResponseMiddleware(next)))

	req := httptest.NewRequest(http.MethodPost, "/update", bytes.NewReader(requestBody))
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set(hashing.Header, hashing.Sum(requestBody, testKey))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if enc := rec.Header().Get("Content-Encoding"); enc != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", enc)
	}

	// The digest must cover the compressed bytes, trailer included, not what
	// the client gets after decompressing them.
	sent := rec.Body.Bytes()
	if got := rec.Header().Get(hashing.Header); got != hashing.Sum(sent, testKey) {
		t.Errorf("response hash = %q, want the digest of the compressed body %q", got, hashing.Sum(sent, testKey))
	}
	if got := rec.Header().Get(hashing.Header); got == hashing.Sum(responseBody, testKey) {
		t.Error("response hash covers the uncompressed body, want the compressed one")
	}

	gr, err := gzip.NewReader(bytes.NewReader(sent))
	if err != nil {
		t.Fatalf("the signed response is not valid gzip: %v", err)
	}
	defer func() {
		_ = gr.Close()
	}()
	decompressed, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("decompressing the response: %v", err)
	}
	if !bytes.Equal(decompressed, responseBody) {
		t.Errorf("decompressed response = %q, want %q", decompressed, responseBody)
	}
}

// TestHashMiddlewareOmitsContentLengthForBodilessStatuses covers the statuses
// that may not carry a body, and so may not carry a Content-Length either.
func TestHashMiddlewareOmitsContentLengthForBodilessStatuses(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusNotModified} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
			})
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			rec := httptest.NewRecorder()

			HashMiddleware(testKey)(next).ServeHTTP(rec, req)

			if rec.Code != status {
				t.Fatalf("status = %d, want %d", rec.Code, status)
			}
			if got := rec.Header().Get("Content-Length"); got != "" {
				t.Errorf("Content-Length = %q, want it absent", got)
			}
		})
	}
}
