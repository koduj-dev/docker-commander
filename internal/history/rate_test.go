package history

import "testing"

func TestIncrease(t *testing.T) {
	cases := []struct {
		name   string
		points []Point
		want   float64
		ok     bool
	}{
		{"empty", nil, 0, false},
		{"single point", []Point{{T: 0, V: 5}}, 0, false},
		{"normal delta", []Point{{T: 0, V: 10}, {T: 1000, V: 40}}, 30, true},
		// A decrease is a counter reset (container recreated) — contributes 0,
		// never a negative delta.
		{"reset then increase", []Point{{T: 0, V: 100}, {T: 1000, V: 20}, {T: 2000, V: 50}}, 30, true},
		{"pure reset, no growth after", []Point{{T: 0, V: 100}, {T: 1000, V: 0}}, 0, true},
		{"flat", []Point{{T: 0, V: 500}, {T: 1000, V: 500}}, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := Increase(c.points)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if ok && got != c.want {
				t.Errorf("Increase = %v, want %v", got, c.want)
			}
		})
	}
}

func TestRateOverWindow(t *testing.T) {
	// 100 bytes over 10 seconds -> 10 bytes/s.
	pts := []Point{{T: 0, V: 0}, {T: 10_000, V: 100}}
	rate, ok := RateOverWindow(pts)
	if !ok || rate != 10 {
		t.Errorf("rate = %v (ok=%v), want 10", rate, ok)
	}

	if _, ok := RateOverWindow([]Point{{T: 0, V: 5}}); ok {
		t.Error("a single point has no rate")
	}
	// Two points at the exact same millisecond: zero elapsed, must not divide by zero.
	if _, ok := RateOverWindow([]Point{{T: 5, V: 1}, {T: 5, V: 9}}); ok {
		t.Error("zero-elapsed window must report ok=false, not divide by zero")
	}
}
