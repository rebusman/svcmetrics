package storage

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
	if err := s.Save(path); err != nil {
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
	if err := loaded.Load(path); err != nil {
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
	if err := s.Save(path); err != nil {
		t.Fatalf("Save error = %v", err)
	}

	loaded := NewMemStorage()
	if err := loaded.Load(path); err != nil {
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

	// The returned maps are snapshots: mutating them must not reach the store.
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
	// Non-nil empty maps, so callers can range over them without a nil check.
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
		if err := s.Load(filepath.Join(t.TempDir(), "nope.json")); err == nil {
			t.Fatal("Load of a missing file returned nil, want error")
		}
	})

	t.Run("malformed json", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "broken.json")
		if err := os.WriteFile(path, []byte("{not json"), 0644); err != nil {
			t.Fatalf("WriteFile error = %v", err)
		}
		s := NewMemStorage()
		if err := s.Load(path); err == nil {
			t.Fatal("Load of malformed JSON returned nil, want error")
		}
	})

	t.Run("empty file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "empty.json")
		if err := os.WriteFile(path, nil, 0644); err != nil {
			t.Fatalf("WriteFile error = %v", err)
		}
		s := NewMemStorage()
		if err := s.Load(path); err == nil {
			t.Fatal("Load of an empty file returned nil, want error")
		}
	})
}

// Entries without a value carry no data, so Load must skip them rather than
// record a zero.
func TestMemStorageLoadSkipsEntriesWithoutValues(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "partial.json")
	body := `[{"id":"NoValue","type":"gauge"},{"id":"NoDelta","type":"counter"},
	          {"id":"Weird","type":"histogram","value":1},{"id":"Good","type":"gauge","value":4.5}]`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}

	s := NewMemStorage()
	if err := s.Load(path); err != nil {
		t.Fatalf("Load error = %v", err)
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

// Run with -race: the handlers hit one MemStorage from many request goroutines.
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

// Save writes through a temporary file, so repeated saves must replace the
// snapshot in place and leave nothing else behind in the directory.
func TestMemStorageSaveOverwritesAndLeavesNoTempFiles(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "metrics.json")

	s := NewMemStorage()
	_, _ = s.UpdateGauge(ctx, "Alloc", 1.5)
	if err := s.Save(path); err != nil {
		t.Fatalf("first Save error = %v", err)
	}

	_, _ = s.UpdateGauge(ctx, "Alloc", 2.5)
	_, _ = s.UpdateCounter(ctx, "PollCount", 4)
	if err := s.Save(path); err != nil {
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
	if err := loaded.Load(path); err != nil {
		t.Fatalf("Load error = %v", err)
	}
	if got, err := loaded.GetGauge(ctx, "Alloc"); err != nil || got != 2.5 {
		t.Fatalf("gauge after overwrite = %v, err = %v, want 2.5, nil", got, err)
	}
	if got, err := loaded.GetCounter(ctx, "PollCount"); err != nil || got != 4 {
		t.Fatalf("counter after overwrite = %d, err = %v, want 4, nil", got, err)
	}
}

// A failed Save must not clobber the snapshot that is already on disk.
func TestMemStorageSaveFailureKeepsPreviousSnapshot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "metrics.json")

	s := NewMemStorage()
	_, _ = s.UpdateGauge(ctx, "Alloc", 7.25)
	if err := s.Save(path); err != nil {
		t.Fatalf("Save error = %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}

	// CreateTemp targets the destination directory, so a missing directory
	// makes Save fail before it can touch the existing file.
	_, _ = s.UpdateGauge(ctx, "Alloc", 99)
	if err := s.Save(filepath.Join(dir, "missing", "metrics.json")); err == nil {
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

// When the rename itself fails, Save must report it and still clean up the
// temporary file it created.
func TestMemStorageSaveCleansUpWhenRenameFails(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// A directory cannot be replaced by a rename, so this makes the last step
	// of Save fail after the temporary file is already written.
	target := filepath.Join(dir, "metrics.json")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatalf("Mkdir error = %v", err)
	}

	s := NewMemStorage()
	_, _ = s.UpdateGauge(ctx, "Alloc", 1)
	if err := s.Save(target); err == nil {
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

	// Writing to an already closed file fails, and writeAndClose must pass
	// that error up rather than report a successful save.
	if err := writeAndClose(f, []byte("payload")); err == nil {
		t.Fatal("writeAndClose on a closed file returned nil, want error")
	}
}
