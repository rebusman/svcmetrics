package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	models "github.com/rebusman/svcmetrics/internal/model"
	"github.com/rebusman/svcmetrics/internal/storage"
)

var listTmpl = template.Must(template.New("metrics").Parse(`
<html>
<head><title>Metrics</title></head>
<body>
	<h1>Metrics</h1>
	<h2>Gauges</h2>
	<ul>
	{{range $name, $value := .Gauges}}
		<li>{{$name}}: {{$value}}</li>
	{{end}}
	</ul>
	<h2>Counters</h2>
	<ul>
	{{range $name, $value := .Counters}}
		<li>{{$name}}: {{$value}}</li>
	{{end}}
	</ul>
</body>
</html>`))

// ValueJSONHandler handles POST /value.
func ValueJSONHandler(s storage.Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var m models.Metrics
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				http.Error(w, "Empty body", http.StatusBadRequest)
			} else {
				http.Error(w, "Invalid JSON", http.StatusBadRequest)
			}
			return
		}

		if m.ID == "" {
			http.Error(w, "Metric ID missing", http.StatusBadRequest)
			return
		}

		ctx := r.Context()

		var result models.Metrics
		result.ID = m.ID
		result.MType = m.MType

		switch m.MType {
		case models.Gauge:
			val, err := s.GetGauge(ctx, m.ID)
			if err != nil {
				writeStorageError(w, err)
				return
			}
			result.Value = &val
		case models.Counter:
			val, err := s.GetCounter(ctx, m.ID)
			if err != nil {
				writeStorageError(w, err)
				return
			}
			result.Delta = &val
		default:
			http.Error(w, "Invalid metric type", http.StatusBadRequest)
			return
		}

		// Encode before committing the response: once the status line is out,
		// http.Error can no longer change it and would only append its message
		// to the JSON body.
		var payload bytes.Buffer
		if err := json.NewEncoder(&payload).Encode(result); err != nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// A failed write here means the client is gone: the status line has
		// shipped, so there is nobody left to report an error to. The
		// ResponseWriter wrapper in cmd/server records it for the request log.
		_, _ = payload.WriteTo(w)
	}
}

// ValueHandler handles GET /value/{type}/{name}.
func ValueHandler(s storage.Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mType := chi.URLParam(r, "type")
		mName := chi.URLParam(r, "name")

		ctx := r.Context()

		var value string
		switch mType {
		case models.Gauge:
			val, err := s.GetGauge(ctx, mName)
			if err != nil {
				writeStorageError(w, err)
				return
			}
			value = strconv.FormatFloat(val, 'f', -1, 64)
		case models.Counter:
			val, err := s.GetCounter(ctx, mName)
			if err != nil {
				writeStorageError(w, err)
				return
			}
			value = strconv.FormatInt(val, 10)
		default:
			http.Error(w, "Invalid metric type", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		// Response already committed; the write error is logged by the
		// ResponseWriter wrapper in cmd/server.
		_, _ = w.Write([]byte(value))
	}
}

// ListHandler handles GET /.
func ListHandler(s storage.Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		gauges, err := s.GetAllGauges(ctx)
		if err != nil {
			writeStorageError(w, err)
			return
		}
		counters, err := s.GetAllCounters(ctx)
		if err != nil {
			writeStorageError(w, err)
			return
		}

		data := struct {
			Gauges   map[string]float64
			Counters map[string]int64
		}{
			Gauges:   gauges,
			Counters: counters,
		}

		// Render into a buffer first: a template that fails halfway through
		// would otherwise leave a partial page that no error can take back.
		var page bytes.Buffer
		if err := listTmpl.Execute(&page, data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		// Response already committed; the write error is logged by the
		// ResponseWriter wrapper in cmd/server.
		_, _ = page.WriteTo(w)
	}
}
