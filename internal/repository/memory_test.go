package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"

	models "github.com/rebusman/svcmetrics/internal/model"
)

func TestMemStorageCounterAccumulates(t *testing.T) {
	ctx := context.Background()
	s := NewMemStorage()

	if got, err := s.UpdateCounter(ctx, "PollCount", 1); err != nil || got != 1 {
		t.Fatalf("first UpdateCounter = %d, err = %v, want 1, nil", got, err)
	}
	if got, err := s.UpdateCounter(ctx, "PollCount", 2); err != nil || got != 3 {
		t.Fatalf("second UpdateCounter = %d, err = %v, want 3, nil", got, err)
	}

	got, err := s.GetCounter(ctx, "PollCount")
	if err != nil {
		t.Fatalf("GetCounter error = %v", err)
	}
	if got != 3 {
		t.Fatalf("counter value = %d, want 3", got)
	}
}

func TestMemStorageMissingMetrics(t *testing.T) {
	ctx := context.Background()
	s := NewMemStorage()

	if _, err := s.GetGauge(ctx, "missing"); err == nil {
		t.Fatal("GetGauge error = nil, want error")
	}
	if _, err := s.GetCounter(ctx, "missing"); err == nil {
		t.Fatal("GetCounter error = nil, want error")
	}
}

func TestMemStorageSaveLoadRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := NewMemStorage()
	_, _ = s.UpdateGauge(ctx, "Alloc", 12.5)
	_, _ = s.UpdateCounter(ctx, "PollCount", 3)

	path := filepath.Join(t.TempDir(), "metrics.json")
	if err := s.Save(ctx, path); err != nil {
		t.Fatalf("Save error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}

	var metrics []models.Metrics
	if err := json.Unmarshal(data, &metrics); err != nil {
		t.Fatalf("json.Unmarshal error = %v", err)
	}
	if len(metrics) != 2 {
		t.Fatalf("metrics count = %d, want 2", len(metrics))
	}

	loaded := NewMemStorage()
	if err := loaded.Load(ctx, path); err != nil {
		t.Fatalf("Load error = %v", err)
	}

	if got, err := loaded.GetGauge(ctx, "Alloc"); err != nil || got != 12.5 {
		t.Fatalf("loaded gauge = %v, err = %v, want 12.5, nil", got, err)
	}
	if got, err := loaded.GetCounter(ctx, "PollCount"); err != nil || got != 3 {
		t.Fatalf("loaded counter = %d, err = %v, want 3, nil", got, err)
	}
}

func TestMemStorageSaveLoadLargeCounter(t *testing.T) {
	ctx := context.Background()
	s := NewMemStorage()
	const bigCounter = int64(math.MaxInt64 - 1)
	_, _ = s.UpdateCounter(ctx, "PollCount", bigCounter)

	path := filepath.Join(t.TempDir(), "metrics_large_counter.json")
	if err := s.Save(ctx, path); err != nil {
		t.Fatalf("Save error = %v", err)
	}

	loaded := NewMemStorage()
	if err := loaded.Load(ctx, path); err != nil {
		t.Fatalf("Load error = %v", err)
	}

	got, err := loaded.GetCounter(ctx, "PollCount")
	if err != nil {
		t.Fatalf("GetCounter error = %v", err)
	}
	if got != bigCounter {
		t.Fatalf("loaded large counter = %d, want %d", got, bigCounter)
	}
}

func TestMemStorageGetAllReturnsIndependentCopies(t *testing.T) {
	ctx := context.Background()
	s := NewMemStorage()
	_, _ = s.UpdateGauge(ctx, "Alloc", 12.5)
	_, _ = s.UpdateGauge(ctx, "Sys", 3)
	_, _ = s.UpdateCounter(ctx, "PollCount", 8)

	gauges, err := s.GetAllGauges(ctx)
	if err != nil {
		t.Fatalf("GetAllGauges error = %v", err)
	}
	if len(gauges) != 2 || gauges["Alloc"] != 12.5 || gauges["Sys"] != 3 {
		t.Fatalf("gauges = %v, want {Alloc:12.5 Sys:3}", gauges)
	}

	counters, err := s.GetAllCounters(ctx)
	if err != nil {
		t.Fatalf("GetAllCounters error = %v", err)
	}
	if len(counters) != 1 || counters["PollCount"] != 8 {
		t.Fatalf("counters = %v, want {PollCount:8}", counters)
	}

	gauges["Alloc"] = 999
	counters["PollCount"] = 999

	if got, _ := s.GetGauge(ctx, "Alloc"); got != 12.5 {
		t.Fatalf("gauge after mutating the returned map = %v, want 12.5", got)
	}
	if got, _ := s.GetCounter(ctx, "PollCount"); got != 8 {
		t.Fatalf("counter after mutating the returned map = %d, want 8", got)
	}
}

func TestMemStorageGetAllOnEmptyStorage(t *testing.T) {
	ctx := context.Background()
	s := NewMemStorage()

	gauges, err := s.GetAllGauges(ctx)
	if err != nil {
		t.Fatalf("GetAllGauges error = %v", err)
	}
	counters, err := s.GetAllCounters(ctx)
	if err != nil {
		t.Fatalf("GetAllCounters error = %v", err)
	}
	if gauges == nil || len(gauges) != 0 {
		t.Fatalf("gauges = %v, want empty non-nil map", gauges)
	}
	if counters == nil || len(counters) != 0 {
		t.Fatalf("counters = %v, want empty non-nil map", counters)
	}
}

func TestMemStorageLoadErrors(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		s := NewMemStorage()
		if err := s.Load(context.Background(), filepath.Join(t.TempDir(), "nope.json")); err == nil {
			t.Fatal("Load of a missing file returned nil, want error")
		}
	})

	t.Run("malformed json", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "broken.json")
		if err := os.WriteFile(path, []byte("{not json"), 0644); err != nil {
			t.Fatalf("WriteFile error = %v", err)
		}
		s := NewMemStorage()
		if err := s.Load(context.Background(), path); err == nil {
			t.Fatal("Load of malformed JSON returned nil, want error")
		}
	})

	t.Run("empty file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "empty.json")
		if err := os.WriteFile(path, nil, 0644); err != nil {
			t.Fatalf("WriteFile error = %v", err)
		}
		s := NewMemStorage()
		if err := s.Load(context.Background(), path); err == nil {
			t.Fatal("Load of an empty file returned nil, want error")
		}
	})
}

// TestMemStorageLoadSkipsEntriesWithoutValues verifies that entries carrying no
// data are skipped rather than recorded as a zero, and that the skips are
// reported as an error wrapping models.ErrInvalidMetric while the valid rest
// of the snapshot is still loaded.
func TestMemStorageLoadSkipsEntriesWithoutValues(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "partial.json")
	body := `[{"id":"NoValue","type":"gauge"},{"id":"NoDelta","type":"counter"},
	          {"id":"Weird","type":"histogram","value":1},{"id":"Good","type":"gauge","value":4.5}]`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}

	s := NewMemStorage()
	if err := s.Load(ctx, path); !errors.Is(err, models.ErrInvalidMetric) {
		t.Fatalf("Load error = %v, want ErrInvalidMetric for the skipped entries", err)
	}

	if _, err := s.GetGauge(ctx, "NoValue"); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("GetGauge(NoValue) error = %v, want ErrNotFound", err)
	}
	if _, err := s.GetCounter(ctx, "NoDelta"); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("GetCounter(NoDelta) error = %v, want ErrNotFound", err)
	}
	if _, err := s.GetGauge(ctx, "Weird"); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("GetGauge(Weird) error = %v, want ErrNotFound", err)
	}
	if got, err := s.GetGauge(ctx, "Good"); err != nil || got != 4.5 {
		t.Fatalf("GetGauge(Good) = %v, err = %v, want 4.5, nil", got, err)
	}
}

// TestMemStorageLoadMergesCounters verifies that loading a snapshot into a
// non-empty storage adds the snapshot deltas to the running totals instead of
// overwriting them, mirroring what UpdateBatch would do.
func TestMemStorageLoadMergesCounters(t *testing.T) {
	ctx := context.Background()

	saved := NewMemStorage()
	_, _ = saved.UpdateCounter(ctx, "PollCount", 5)
	_, _ = saved.UpdateGauge(ctx, "Alloc", 1.5)

	path := filepath.Join(t.TempDir(), "metrics.json")
	if err := saved.Save(ctx, path); err != nil {
		t.Fatalf("Save error = %v", err)
	}

	s := NewMemStorage()
	_, _ = s.UpdateCounter(ctx, "PollCount", 7)
	_, _ = s.UpdateGauge(ctx, "Alloc", 9.5)

	if err := s.Load(ctx, path); err != nil {
		t.Fatalf("Load error = %v", err)
	}

	if got, err := s.GetCounter(ctx, "PollCount"); err != nil || got != 12 {
		t.Fatalf("counter after Load = %d, err = %v, want 12 (7+5)", got, err)
	}
	if got, err := s.GetGauge(ctx, "Alloc"); err != nil || got != 1.5 {
		t.Fatalf("gauge after Load = %v, err = %v, want the snapshot value 1.5", got, err)
	}
}

// TestMemStorageConcurrentAccess hits one MemStorage from many goroutines, the
// way the handlers do. Run it with -race.
func TestMemStorageConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	s := NewMemStorage()

	const (
		workers   = 8
		perWorker = 200
	)

	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := range perWorker {
				_, _ = s.UpdateCounter(ctx, "PollCount", 1)
				_, _ = s.UpdateGauge(ctx, fmt.Sprintf("Gauge%d", w), float64(i))
				_, _ = s.GetCounter(ctx, "PollCount")
				_, _ = s.GetAllGauges(ctx)
			}
		}(w)
	}
	wg.Wait()

	got, err := s.GetCounter(ctx, "PollCount")
	if err != nil {
		t.Fatalf("GetCounter error = %v", err)
	}
	if want := int64(workers * perWorker); got != want {
		t.Fatalf("counter = %d, want %d", got, want)
	}
}

// TestMemStorageSaveOverwritesAndLeavesNoTempFiles verifies that repeated saves
// replace the snapshot in place and leave nothing else in the directory: Save
// writes through a temporary file.
func TestMemStorageSaveOverwritesAndLeavesNoTempFiles(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "metrics.json")

	s := NewMemStorage()
	_, _ = s.UpdateGauge(ctx, "Alloc", 1.5)
	if err := s.Save(ctx, path); err != nil {
		t.Fatalf("first Save error = %v", err)
	}

	_, _ = s.UpdateGauge(ctx, "Alloc", 2.5)
	_, _ = s.UpdateCounter(ctx, "PollCount", 4)
	if err := s.Save(ctx, path); err != nil {
		t.Fatalf("second Save error = %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir error = %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "metrics.json" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("directory contains %v, want only metrics.json", names)
	}

	loaded := NewMemStorage()
	if err := loaded.Load(ctx, path); err != nil {
		t.Fatalf("Load error = %v", err)
	}
	if got, err := loaded.GetGauge(ctx, "Alloc"); err != nil || got != 2.5 {
		t.Fatalf("gauge after overwrite = %v, err = %v, want 2.5, nil", got, err)
	}
	if got, err := loaded.GetCounter(ctx, "PollCount"); err != nil || got != 4 {
		t.Fatalf("counter after overwrite = %d, err = %v, want 4, nil", got, err)
	}
}

// TestMemStorageSaveFailureKeepsPreviousSnapshot verifies that a failed Save
// does not clobber the snapshot already on disk.
func TestMemStorageSaveFailureKeepsPreviousSnapshot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "metrics.json")

	s := NewMemStorage()
	_, _ = s.UpdateGauge(ctx, "Alloc", 7.25)
	if err := s.Save(ctx, path); err != nil {
		t.Fatalf("Save error = %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}

	_, _ = s.UpdateGauge(ctx, "Alloc", 99)
	if err := s.Save(ctx, filepath.Join(dir, "missing", "metrics.json")); err == nil {
		t.Fatal("Save into a missing directory returned nil, want error")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile after failed Save error = %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("snapshot changed after a failed Save:\nbefore = %s\nafter  = %s", before, after)
	}
}

// TestMemStorageSaveCleansUpWhenRenameFails verifies that a failing rename is
// reported and the temporary file is still cleaned up.
func TestMemStorageSaveCleansUpWhenRenameFails(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	target := filepath.Join(dir, "metrics.json")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatalf("Mkdir error = %v", err)
	}

	s := NewMemStorage()
	_, _ = s.UpdateGauge(ctx, "Alloc", 1)
	if err := s.Save(ctx, target); err == nil {
		t.Fatal("Save over a directory returned nil, want error")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir error = %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "metrics.json" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("directory contains %v, want only the pre-existing metrics.json", names)
	}
}

func TestWriteAndCloseReportsWriteError(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "closed-*")
	if err != nil {
		t.Fatalf("CreateTemp error = %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}

	if err := writeAndClose(f, []byte("payload")); err == nil {
		t.Fatal("writeAndClose on a closed file returned nil, want error")
	}
}

func TestMemStorageUpdateBatch(t *testing.T) {
	ctx := context.Background()
	s := NewMemStorage()

	if _, err := s.UpdateCounter(ctx, "PollCount", 10); err != nil {
		t.Fatalf("UpdateCounter error = %v", err)
	}

	err := s.UpdateBatch(ctx, []models.Metrics{
		gauge("Alloc", 1.5),
		counter("PollCount", 5),
		gauge("Alloc", 2.5),
		counter("PollCount", 2),
	})
	if err != nil {
		t.Fatalf("UpdateBatch error = %v", err)
	}

	if got, err := s.GetGauge(ctx, "Alloc"); err != nil || got != 2.5 {
		t.Errorf("Alloc = %v (err %v), want the last value 2.5", got, err)
	}
	if got, err := s.GetCounter(ctx, "PollCount"); err != nil || got != 17 {
		t.Errorf("PollCount = %v (err %v), want 17 (10+5+2)", got, err)
	}
}

func TestMemStorageUpdateBatchEmpty(t *testing.T) {
	if err := NewMemStorage().UpdateBatch(context.Background(), nil); err != nil {
		t.Fatalf("UpdateBatch(nil) error = %v", err)
	}
}

// TestMemStorageUpdateBatchIsAllOrNothing verifies that a batch which cannot be
// applied in full is not applied at all.
func TestMemStorageUpdateBatchIsAllOrNothing(t *testing.T) {
	ctx := context.Background()
	s := NewMemStorage()

	err := s.UpdateBatch(ctx, []models.Metrics{
		gauge("Alloc", 1.5),
		{ID: "Broken", MType: "histogram"},
	})
	if !errors.Is(err, models.ErrInvalidMetric) {
		t.Fatalf("UpdateBatch error = %v, want ErrInvalidMetric", err)
	}

	if _, err := s.GetGauge(ctx, "Alloc"); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("Alloc error = %v, want ErrNotFound — the batch was rejected", err)
	}
}

// TestMemStorageUpdateBatchIsAtomicForReaders verifies that readers never
// observe a partially applied batch: the whole batch is written under one
// lock.
func TestMemStorageUpdateBatchIsAtomicForReaders(t *testing.T) {
	ctx := context.Background()
	s := NewMemStorage()

	const rounds = 200
	names := []string{"A", "B", "C", "D"}

	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 1; i <= rounds; i++ {
			batch := make([]models.Metrics, 0, len(names))
			for _, name := range names {
				batch = append(batch, gauge(name, float64(i)))
			}
			if err := s.UpdateBatch(ctx, batch); err != nil {
				t.Errorf("UpdateBatch error = %v", err)
				return
			}
		}
	})

	var mismatches int
	for range rounds * 10 {
		gauges, err := s.GetAllGauges(ctx)
		if err != nil {
			t.Fatalf("GetAllGauges error = %v", err)
		}
		if len(gauges) == 0 {
			continue
		}
		if len(gauges) != len(names) {
			mismatches++
			continue
		}
		first := gauges[names[0]]
		for _, name := range names[1:] {
			if gauges[name] != first {
				mismatches++
				break
			}
		}
	}

	wg.Wait()

	if mismatches != 0 {
		t.Fatalf("observed %d partially applied batches, want 0", mismatches)
	}
}

// TestMemStorageUpdateBatchConcurrentCounters verifies that concurrent batches
// lose no counter increments.
func TestMemStorageUpdateBatchConcurrentCounters(t *testing.T) {
	ctx := context.Background()
	s := NewMemStorage()

	const (
		writers = 8
		rounds  = 100
	)

	var wg sync.WaitGroup
	for range writers {
		wg.Go(func() {
			for range rounds {
				if err := s.UpdateBatch(ctx, []models.Metrics{counter("PollCount", 1)}); err != nil {
					t.Errorf("UpdateBatch error = %v", err)
					return
				}
			}
		})
	}
	wg.Wait()

	got, err := s.GetCounter(ctx, "PollCount")
	if err != nil {
		t.Fatalf("GetCounter error = %v", err)
	}
	if want := int64(writers * rounds); got != want {
		t.Fatalf("PollCount = %d, want %d", got, want)
	}
}
