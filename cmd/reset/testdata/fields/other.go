package fields

type (
	// generate:reset
	Grouped struct {
		values []float64
		total  float64
	}

	// Ungrouped has no marker and must be left alone.
	Ungrouped struct {
		n int
	}
)
