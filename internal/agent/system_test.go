package agent

import (
	"context"
	"strconv"
	"strings"
	"testing"

	models "github.com/rebusman/svcmetrics/internal/model"
)

// TestCollectSystemMetrics verifies that the host collector fills in the memory
// gauges and one CPUutilization gauge per CPU, numbered from one without gaps —
// a gap would mean the agent reports a CPU the server cannot place.
func TestCollectSystemMetrics(t *testing.T) {
	a := New("", 0, 0, 0, "", 0)

	if err := a.CollectSystemMetrics(context.Background()); err != nil {
		t.Fatalf("CollectSystemMetrics() error = %v", err)
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	total, ok := a.metrics.gauges[models.TotalMemory]
	if !ok {
		t.Fatal("TotalMemory was not collected")
	}
	if total <= 0 {
		t.Errorf("TotalMemory = %v, want a positive amount of bytes", total)
	}

	free, ok := a.metrics.gauges[models.FreeMemory]
	if !ok {
		t.Fatal("FreeMemory was not collected")
	}
	if free < 0 || free > total {
		t.Errorf("FreeMemory = %v, want a value within the total %v", free, total)
	}

	numbers := make(map[int]bool)
	for name, value := range a.metrics.gauges {
		suffix, found := strings.CutPrefix(name, "CPUutilization")
		if !found {
			continue
		}
		n, err := strconv.Atoi(suffix)
		if err != nil {
			t.Errorf("gauge %q does not end in a CPU number", name)
			continue
		}
		numbers[n] = true
		if value < 0 || value > 100 {
			t.Errorf("%s = %v, want a percentage", name, value)
		}
	}

	if len(numbers) == 0 {
		t.Fatal("no CPUutilization gauge was collected")
	}
	for n := 1; n <= len(numbers); n++ {
		if !numbers[n] {
			t.Errorf("%s is missing from the %d collected CPUs", models.CPUUtilization(n), len(numbers))
		}
	}
}

// TestCollectSystemMetricsDropsVanishedCPUs verifies that a reading finding
// fewer CPUs than the previous one removes the gauges of the CPUs that went
// away. Without that, a host whose cpuset is narrowed at run time would keep
// reporting the load those CPUs had at the moment they disappeared.
//
// The state is set up by hand rather than by narrowing the real cpuset: the
// number of CPUs gopsutil reports is not something a test can dictate.
func TestCollectSystemMetricsDropsVanishedCPUs(t *testing.T) {
	a := New("", 0, 0, 0, "", 0)

	const vanished = 512
	a.mu.Lock()
	for i := 1; i <= vanished; i++ {
		a.metrics.gauges[models.CPUUtilization(i)] = 100
	}
	a.cpuCount = vanished
	a.mu.Unlock()

	if err := a.CollectSystemMetrics(context.Background()); err != nil {
		t.Fatalf("CollectSystemMetrics() error = %v", err)
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.cpuCount <= 0 {
		t.Fatalf("cpuCount = %d, want the number of CPUs just read", a.cpuCount)
	}
	if a.cpuCount >= vanished {
		t.Skipf("the host reports %d CPUs, nothing vanished", a.cpuCount)
	}

	for i := a.cpuCount + 1; i <= vanished; i++ {
		if _, ok := a.metrics.gauges[models.CPUUtilization(i)]; ok {
			t.Errorf("%s outlived the CPU it measured", models.CPUUtilization(i))
		}
	}
	for i := 1; i <= a.cpuCount; i++ {
		if _, ok := a.metrics.gauges[models.CPUUtilization(i)]; !ok {
			t.Errorf("%s is missing from the %d CPUs read", models.CPUUtilization(i), a.cpuCount)
		}
	}
}

// TestCollectSystemMetricsLeavesTheCounterAlone verifies that reading the host
// does not advance PollCount: the counter belongs to the runtime poller, and
// two collectors bumping it would report twice the polls that happened.
func TestCollectSystemMetricsLeavesTheCounterAlone(t *testing.T) {
	a := New("", 0, 0, 0, "", 0)

	if err := a.CollectSystemMetrics(context.Background()); err != nil {
		t.Fatalf("CollectSystemMetrics() error = %v", err)
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	if got := a.metrics.counters[models.PollCount]; got != 0 {
		t.Fatalf("PollCount = %d, want 0", got)
	}
}

// TestCollectBatchIncludesSystemMetrics verifies that the gauges the host
// collector produced reach the wire: the batch is no longer built from a fixed
// list of names, so a metric whose name is only known at run time must still
// make it in.
func TestCollectBatchIncludesSystemMetrics(t *testing.T) {
	a := New("", 0, 0, 0, "", 0)

	a.CollectRuntimeMetrics()
	if err := a.CollectSystemMetrics(context.Background()); err != nil {
		t.Fatalf("CollectSystemMetrics() error = %v", err)
	}

	batch := a.collectBatch()

	sent := make(map[string]bool, len(batch))
	for _, m := range batch {
		sent[m.ID] = true
	}

	for _, name := range models.SystemGaugeMetricNames {
		if !sent[name] {
			t.Errorf("%s never reached the batch", name)
		}
	}
	if !sent[models.CPUUtilization(1)] {
		t.Errorf("%s never reached the batch", models.CPUUtilization(1))
	}
	for _, name := range models.RuntimeGaugeMetricNames {
		if !sent[name] {
			t.Errorf("%s never reached the batch", name)
		}
	}
}
