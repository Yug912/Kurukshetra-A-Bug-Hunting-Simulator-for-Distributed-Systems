package rng

import "testing"

func first(n int, f func() uint64) []uint64 {
	out := make([]uint64, n)
	for i := range out {
		out[i] = f()
	}
	return out
}

func eq(a, b []uint64) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSameSeed(t *testing.T) {
	a := New(7).Stream(StreamFaults)
	b := New(7).Stream(StreamFaults)
	if !eq(first(100, a.Uint64), first(100, b.Uint64)) {
		t.Fatal("same seed differs")
	}
}

func TestStreamsDiffer(t *testing.T) {
	s := New(7)
	if eq(first(10, s.Stream(StreamFaults).Uint64), first(10, s.Stream(StreamMessages).Uint64)) {
		t.Fatal("streams equal")
	}
}

func TestKeyedOrderIndependent(t *testing.T) {
	s := New(3)
	a1 := s.Keyed(StreamMessages, "a").Uint64()
	b1 := s.Keyed(StreamMessages, "b").Uint64()
	b2 := s.Keyed(StreamMessages, "b").Uint64()
	a2 := s.Keyed(StreamMessages, "a").Uint64()
	if a1 != a2 || b1 != b2 || a1 == b1 {
		t.Fatal("keyed not order independent")
	}
}

func TestBetween(t *testing.T) {
	r := New(1).Stream("x")
	if Between(r, [2]int64{5, 5}) != 5 {
		t.Fatal("lo==hi")
	}
	for i := 0; i < 1000; i++ {
		v := Between(r, [2]int64{-3, 3})
		if v < -3 || v > 3 {
			t.Fatalf("out of range %d", v)
		}
	}
}
