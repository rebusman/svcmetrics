package fields

import (
	"bytes"
	nh "net/http"
	"strings"
	"time"
)

type Status int

type Tags []string

type Labels map[string]string

// Self points to itself; resetting it must stop at maxResetDepth.
type Self *Self

type Plain struct {
	n int
}

// generate:reset
type Inner struct {
	name string
}

type Custom struct {
	v int
}

func (c *Custom) Reset() { c.v = -1 }

// generate:reset
type Fields struct {
	b       bool
	s       string
	i, j    int
	u8      uint8
	f       float64
	c       complex128
	r       rune
	_       int
	status  Status
	slice   []int
	tags    Tags
	m       map[string]int
	labels  Labels
	arr     [3]int
	ptr     *int
	pptr    **string
	plain   Plain
	plainP  *Plain
	inner   Inner
	innerP  *Inner
	custom  Custom
	anon    struct{ x int }
	fn      func()
	ch      chan int
	iface   interface{ Do() }
	a       any
	err     error
	buf     bytes.Buffer
	sb      *strings.Builder
	header  nh.Header
	timeout time.Duration
	self    Self
	Plain
	*Custom
}
