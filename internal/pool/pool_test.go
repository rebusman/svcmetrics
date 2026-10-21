package pool

import "testing"

type buffer struct {
	data []byte
}

func (b *buffer) Reset() {
	b.data = b.data[:0]
}

func TestPool(t *testing.T) {
	p := New(func() *buffer { return &buffer{data: make([]byte, 0, 16)} })

	b := p.Get()
	if b == nil {
		t.Fatal("Get returned nil")
	}
	b.data = append(b.data, "hello"...)

	p.Put(b)
	if len(b.data) != 0 {
		t.Fatalf("Put did not reset object: len=%d", len(b.data))
	}

	got := p.Get()
	if len(got.data) != 0 {
		t.Fatalf("Get returned dirty object: len=%d", len(got.data))
	}
}
