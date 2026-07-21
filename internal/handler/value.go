package handler

import (
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	models "github.com/rebusman/svcmetrics/internal/model"
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
func ValueJSONHandler(s Storage) http.HandlerFunc {
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

		var result models.Metrics
		result.ID = m.ID
		result.MType = m.MType

		switch m.MType {
		case models.Gauge:
			val, err := s.GetGauge(m.ID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			result.Value = &val
		case models.Counter:
			val, err := s.GetCounter(m.ID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			result.Delta = &val
		default:
			http.Error(w, "Invalid metric type", http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(result); err != nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	}
}

// ValueHandler handles GET /value/{type}/{name}.
func ValueHandler(s Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mType := chi.URLParam(r, "type")
		mName := chi.URLParam(r, "name")

		var value string
		switch mType {
		case models.Gauge:
			val, err := s.GetGauge(mName)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			value = strconv.FormatFloat(val, 'f', -1, 64)
		case models.Counter:
			val, err := s.GetCounter(mName)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			value = strconv.FormatInt(val, 10)
		default:
			http.Error(w, "Invalid metric type", http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(value)); err != nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
	}
}

// ListHandler handles GET /.
func ListHandler(s Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := struct {
			Gauges   map[string]float64
			Counters map[string]int64
		}{
			Gauges:   s.GetAllGauges(),
			Counters: s.GetAllCounters(),
		}

		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		if err := listTmpl.Execute(w, data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}
