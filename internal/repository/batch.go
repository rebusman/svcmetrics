package repository

import (
	"fmt"
	"sort"

	models "github.com/rebusman/svcmetrics/internal/model"
)

// batchKey identifies a metric inside a batch: the same name may exist as both
// a gauge and a counter.
type batchKey struct {
	mtype string
	id    string
}

// aggregateBatch validates a batch and folds it into one entry per metric:
// gauges keep the last value, counter deltas are summed. The result is sorted
// by (type, name) so that every storage writes the metrics in the same order —
// two concurrent transactions then take the row locks in the same sequence and
// cannot deadlock on each other. It returns models.ErrInvalidMetric for a
// metric without an ID, of an unknown type or without the value its type
// requires.
//
// The returned metrics never share the Delta and Value pointers with the input,
// so the caller is free to keep using the slice it passed in.
func aggregateBatch(metrics []models.Metrics) ([]models.Metrics, error) {
	index := make(map[batchKey]int, len(metrics))
	out := make([]models.Metrics, 0, len(metrics))

	for _, m := range metrics {
		if m.ID == "" {
			return nil, fmt.Errorf("%w: metric ID missing", models.ErrInvalidMetric)
		}

		switch m.MType {
		case models.Gauge:
			if m.Value == nil {
				return nil, fmt.Errorf("%w: value missing for gauge %q", models.ErrInvalidMetric, m.ID)
			}
		case models.Counter:
			if m.Delta == nil {
				return nil, fmt.Errorf("%w: delta missing for counter %q", models.ErrInvalidMetric, m.ID)
			}
		default:
			return nil, fmt.Errorf("%w: unknown type %q for metric %q", models.ErrInvalidMetric, m.MType, m.ID)
		}

		key := batchKey{mtype: m.MType, id: m.ID}
		pos, seen := index[key]
		if !seen {
			out = append(out, copyMetric(m))
			index[key] = len(out) - 1
			continue
		}

		if m.MType == models.Gauge {
			value := *m.Value
			out[pos].Value = &value
			continue
		}
		delta := *out[pos].Delta + *m.Delta
		out[pos].Delta = &delta
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].MType != out[j].MType {
			return out[i].MType < out[j].MType
		}
		return out[i].ID < out[j].ID
	})

	return out, nil
}

// copyMetric returns a copy of m that shares no pointers with it.
func copyMetric(m models.Metrics) models.Metrics {
	c := models.Metrics{ID: m.ID, MType: m.MType}
	if m.Value != nil {
		value := *m.Value
		c.Value = &value
	}
	if m.Delta != nil {
		delta := *m.Delta
		c.Delta = &delta
	}
	return c
}
