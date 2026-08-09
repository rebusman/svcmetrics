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
	"github.com/rebusman/svcmetrics/internal/repository"
)

// listTmpl renders the HTML page served by ListHandler.
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

// ValueJSONHandler handles POST /value: a metric lookup by ID and type,
// answered in JSON. It replies 404 when the metric is unknown.
func ValueJSONHandler(s repository.Storage) http.HandlerFunc {
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

		var payload bytes.Buffer
		if err := json.NewEncoder(&payload).Encode(result); err != nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = payload.WriteTo(w)
	}
}

// ValueHandler handles GET /value/{type}/{name}: a metric lookup answered as
// plain text.
func ValueHandler(s repository.Storage) http.HandlerFunc {
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
		_, _ = w.Write([]byte(value))
	}
}

// ListHandler handles GET /: an HTML page listing every stored metric.
func ListHandler(s repository.Storage) http.HandlerFunc {
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

		var page bytes.Buffer
		if err := listTmpl.Execute(&page, data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = page.WriteTo(w)
	}
}
