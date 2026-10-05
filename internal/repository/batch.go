package repository

import (
	"cmp"
	"fmt"
	"slices"

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
// so the caller is free to keep using the slice it passed in. The values they
// point at live in two slices allocated up front, one slot per input metric,
// rather than in an allocation of their own each.
func aggregateBatch(metrics []models.Metrics) ([]models.Metrics, error) {
	index := make(map[batchKey]int, len(metrics))
	out := make([]models.Metrics, 0, len(metrics))
	values := make([]float64, 0, len(metrics))
	deltas := make([]int64, 0, len(metrics))

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
		if seen {
			// The slot is the merged metric's own, so it is updated in place.
			if m.MType == models.Gauge {
				*out[pos].Value = *m.Value
			} else {
				*out[pos].Delta += *m.Delta
			}
			continue
		}

		// The value slices never grow past their capacity, so a pointer into
		// them stays valid for as long as the result lives.
		merged := models.Metrics{ID: m.ID, MType: m.MType}
		if m.MType == models.Gauge {
			values = append(values, *m.Value)
			merged.Value = &values[len(values)-1]
		} else {
			deltas = append(deltas, *m.Delta)
			merged.Delta = &deltas[len(deltas)-1]
		}
		out = append(out, merged)
		index[key] = len(out) - 1
	}

	slices.SortFunc(out, func(a, b models.Metrics) int {
		return cmp.Or(cmp.Compare(a.MType, b.MType), cmp.Compare(a.ID, b.ID))
	})

	return out, nil
}
