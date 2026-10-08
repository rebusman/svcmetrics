package handler_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/rebusman/svcmetrics/internal/handler"
	"github.com/rebusman/svcmetrics/internal/hashing"
	"github.com/rebusman/svcmetrics/internal/repository"
)

// newRouter mounts the handlers on the same routes the server uses. The path
// handlers read their parameters through chi, so they need its router.
func newRouter(s repository.Storage, p handler.Pinger) http.Handler {
	r := chi.NewRouter()
	r.Post("/update", handler.UpdateJSONHandler(s, nil))
	r.Post("/updates/", handler.UpdatesJSONHandler(s, nil))
	r.Post("/update/{type}/{name}/{value}", handler.UpdateHandler(s, nil))
	r.Get("/value/{type}/{name}", handler.ValueHandler(s))
	r.Post("/value", handler.ValueJSONHandler(s))
	r.Get("/ping", handler.PingHandler(p))
	r.Get("/", handler.ListHandler(s))
	return r
}

// do sends one request to h and prints the status and the body of the answer.
func do(h http.Handler, method, target, body string) {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if answer := strings.TrimSpace(w.Body.String()); answer != "" {
		fmt.Println(w.Code, answer)
	} else {
		fmt.Println(w.Code)
	}
}

// A metric passed in the path: the type, the name and the value. A counter
// adds the value to its total, a gauge replaces its value.
func ExampleUpdateHandler() {
	h := newRouter(repository.NewMemStorage(), nil)

	do(h, http.MethodPost, "/update/counter/PollCount/5", "")
	do(h, http.MethodPost, "/update/counter/PollCount/3", "")
	do(h, http.MethodPost, "/update/gauge/Alloc/123.5", "")
	do(h, http.MethodGet, "/value/counter/PollCount", "")

	// An unknown type and a value that is not a number are rejected.
	do(h, http.MethodPost, "/update/histogram/Alloc/1", "")
	do(h, http.MethodPost, "/update/gauge/Alloc/abc", "")

	// Output:
	// 200
	// 200
	// 200
	// 200 8
	// 400 Invalid metric type
	// 400 Invalid gauge value
}

// A single metric in JSON. The answer carries the metric as stored: for a
// counter, delta is the running total rather than the increment sent.
func ExampleUpdateJSONHandler() {
	h := newRouter(repository.NewMemStorage(), nil)

	do(h, http.MethodPost, "/update", `{"id":"PollCount","type":"counter","delta":5}`)
	do(h, http.MethodPost, "/update", `{"id":"PollCount","type":"counter","delta":3}`)
	do(h, http.MethodPost, "/update", `{"id":"Alloc","type":"gauge","value":123.5}`)

	// A gauge without a value is rejected.
	do(h, http.MethodPost, "/update", `{"id":"Alloc","type":"gauge"}`)

	// Output:
	// 200 {"id":"PollCount","type":"counter","delta":5}
	// 200 {"id":"PollCount","type":"counter","delta":8}
	// 200 {"id":"Alloc","type":"gauge","value":123.5}
	// 400 Value missing for gauge
}

// A batch of metrics stored in one atomic write. Repeated counters add up and
// a repeated gauge keeps its last value. A batch with one bad metric is
// rejected in full.
func ExampleUpdatesJSONHandler() {
	s := repository.NewMemStorage()
	h := newRouter(s, nil)

	do(h, http.MethodPost, "/updates/", `[
		{"id":"PollCount","type":"counter","delta":2},
		{"id":"PollCount","type":"counter","delta":3},
		{"id":"Alloc","type":"gauge","value":1.5},
		{"id":"Alloc","type":"gauge","value":2.5}
	]`)
	do(h, http.MethodGet, "/value/counter/PollCount", "")
	do(h, http.MethodGet, "/value/gauge/Alloc", "")

	do(h, http.MethodPost, "/updates/", `[
		{"id":"PollCount","type":"counter","delta":100},
		{"id":"Broken","type":"gauge"}
	]`)
	do(h, http.MethodGet, "/value/counter/PollCount", "")

	// Output:
	// 200 {"status":"ok"}
	// 200 5
	// 200 2.5
	// 400 invalid metric: value missing for gauge "Broken"
	// 200 5
}

// A metric looked up by type and name, answered as plain text.
func ExampleValueHandler() {
	h := newRouter(repository.NewMemStorage(), nil)

	do(h, http.MethodPost, "/update/gauge/Alloc/123.5", "")
	do(h, http.MethodGet, "/value/gauge/Alloc", "")
	do(h, http.MethodGet, "/value/gauge/Unknown", "")

	// Output:
	// 200
	// 200 123.5
	// 404 gauge "Unknown": metric not found
}

// A metric looked up in JSON: the request names the metric, the answer adds its
// value.
func ExampleValueJSONHandler() {
	h := newRouter(repository.NewMemStorage(), nil)

	do(h, http.MethodPost, "/update/counter/PollCount/7", "")
	do(h, http.MethodPost, "/value", `{"id":"PollCount","type":"counter"}`)
	do(h, http.MethodPost, "/value", `{"id":"Unknown","type":"counter"}`)

	// Output:
	// 200
	// 200 {"id":"PollCount","type":"counter","delta":7}
	// 404 counter "Unknown": metric not found
}

// The HTML page listing every stored metric.
func ExampleListHandler() {
	h := newRouter(repository.NewMemStorage(), nil)
	do(h, http.MethodPost, "/update/gauge/Alloc/123.5", "")

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	fmt.Println(w.Code, w.Header().Get("Content-Type"))
	fmt.Println(strings.Contains(w.Body.String(), "<li>Alloc: 123.5</li>"))

	// Output:
	// 200
	// 200 text/html
	// true
}

// pinger is a database connection that is either alive or not.
type pinger struct{ err error }

func (p pinger) PingContext(context.Context) error { return p.err }

// The database health check: 200 while the database answers, 500 when it does
// not or when the server runs without one.
func ExamplePingHandler() {
	do(newRouter(nil, pinger{}), http.MethodGet, "/ping", "")
	do(newRouter(nil, pinger{err: errors.New("connection refused")}), http.MethodGet, "/ping", "")
	do(newRouter(nil, nil), http.MethodGet, "/ping", "")

	// Output:
	// 200
	// 500 connection refused
	// 500 Database is not configured
}

// With a key, a signed request is verified before it reaches the handler, and
// every response is signed. A request with a wrong signature is rejected.
func ExampleHashMiddleware() {
	const key = "secret"
	h := handler.HashMiddleware(key)(newRouter(repository.NewMemStorage(), nil))

	send := func(sig string) {
		body := `{"id":"Alloc","type":"gauge","value":1}`
		r := httptest.NewRequest(http.MethodPost, "/update", strings.NewReader(body))
		r.Header.Set(hashing.Header, sig)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		answer, _ := io.ReadAll(w.Body)
		signed := w.Header().Get(hashing.Header) == hashing.Sum(answer, key)
		fmt.Println(w.Code, strings.TrimSpace(string(answer)), "signed:", signed)
	}

	send(hashing.Sum([]byte(`{"id":"Alloc","type":"gauge","value":1}`), key))
	send(hashing.Sum([]byte("something else"), key))

	// Output:
	// 200 {"id":"Alloc","type":"gauge","value":1} signed: true
	// 400 Invalid request hash signed: true
}
