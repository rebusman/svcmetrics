package generic

type Box[T any] struct {
	v T
}

// generate:reset
type Pair[K comparable, V any] struct {
	key   K
	val   V
	items []V
	index map[K]int
	box   Box[V]
}
