package agent

import (
	"context"
	"fmt"
	"maps"

	models "github.com/rebusman/svcmetrics/internal/model"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
)

// CollectSystemMetrics reads the memory and per-CPU statistics of the host into
// the agent's state. It reports the first failure it meets and leaves the
// previously collected values alone, so a statistic that could not be read once
// keeps its last known value instead of dropping to zero.
//
// How many CPUutilization gauges there are is decided here rather than at
// build time: the count follows what gopsutil reports for this host.
func (a *Agent) CollectSystemMetrics(ctx context.Context) error {
	vm, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return fmt.Errorf("reading the memory statistics: %w", err)
	}

	// An interval of zero measures the load since the previous call instead of
	// sleeping through a sampling window of its own: the poll ticker sets the
	// window, and a cancelled context leaves no goroutine waiting out an
	// interval nobody is going to read.
	loads, err := cpu.PercentWithContext(ctx, 0, true)
	if err != nil {
		return fmt.Errorf("reading the CPU utilization: %w", err)
	}

	values := make(map[string]float64, len(loads)+len(models.SystemGaugeMetricNames))
	values[models.TotalMemory] = float64(vm.Total)
	values[models.FreeMemory] = float64(vm.Free)
	for i, load := range loads {
		values[models.CPUUtilization(i+1)] = load
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	maps.Copy(a.metrics.gauges, values)
	return nil
}
