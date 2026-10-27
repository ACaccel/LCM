package engine

import (
	"strconv"
	"testing"
)

func TestSchedulerRoundRobin(t *testing.T) {
	s := newScheduler()
	wide, small, other := &run{id: "wide"}, &run{id: "small"}, &run{id: "other"}
	for i := range 50 {
		s.push(wide, i)
	}
	s.push(small, 0)
	s.push(other, 0)
	s.push(small, 1)
	var order []string
	for range 6 {
		r, i, ok := s.pop()
		if !ok {
			t.Fatal("pop failed")
		}
		order = append(order, r.id+":"+strconv.Itoa(i))
	}
	want := []string{"wide:0", "small:0", "other:0", "wide:1", "small:1", "wide:2"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
	s.stop()
	if _, _, ok := s.pop(); ok {
		t.Fatal("pop after stop")
	}
	s.push(wide, 1) // ignored after stop
}
