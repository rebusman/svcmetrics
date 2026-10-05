package agent

import (
	"context"
	"fmt"

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
// build time: the count follows what gopsutil reports for this host, and it can
// change while the agent runs — a cpuset or a CPU quota may be narrowed under a
// container. A reading that finds fewer CPUs than the previous one therefore
// deletes the gauges of the CPUs that went away, so a load measured before the
// change is not reported for the rest of the agent's life.
//
// Both statistics are read before the lock is taken, so the values go straight
// into the state under it: a handful of map writes and no intermediate map to
// allocate and copy from.
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

	a.mu.Lock()
	defer a.mu.Unlock()

	a.metrics.gauges[models.TotalMemory] = float64(vm.Total)
	a.metrics.gauges[models.FreeMemory] = float64(vm.Free)
	for i, load := range loads {
		a.metrics.gauges[models.CPUUtilization(i+1)] = load
	}
	for i := len(loads); i < a.cpuCount; i++ {
		delete(a.metrics.gauges, models.CPUUtilization(i+1))
	}
	a.cpuCount = len(loads)

	return nil
}
