package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/rebusman/svcmetrics/internal/storage"
)

func UpdateHandler(s storage.Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// URL format: /update/<TYPE>/<NAME>/<VALUE>
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")

		if len(parts) < 2 || parts[0] != "update" {
			http.Error(w, "Invalid path", http.StatusNotFound)
			return
		}

		if len(parts) < 3 {
			http.Error(w, "Metric name missing", http.StatusNotFound)
			return
		}

		if len(parts) < 4 {
			http.Error(w, "Metric value missing", http.StatusBadRequest)
			return
		}

		mType := parts[1]
		mName := parts[2]
		mValueStr := parts[3]

		switch mType {
		case "gauge":
			val, err := strconv.ParseFloat(mValueStr, 64)
			if err != nil {
				http.Error(w, "Invalid gauge value", http.StatusBadRequest)
				return
			}
			s.UpdateGauge(mName, val)
		case "counter":
			val, err := strconv.ParseInt(mValueStr, 10, 64)
			if err != nil {
				http.Error(w, "Invalid counter value", http.StatusBadRequest)
				return
			}
			s.UpdateCounter(mName, val)
		default:
			http.Error(w, "Invalid metric type", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
	}
}
