package history

// Increase returns the total increase across a series of cumulative points,
// assumed sorted oldest-first (as Query/QueryAll already return them). A
// decrease between two consecutive points means the container was recreated
// and the counter restarted — that step contributes 0, never a negative
// delta, the same reset convention monitor.applyNetRates and the frontend's
// netRates() already use. ok is false with fewer than two points, since a
// single sample has no "increase" to speak of.
func Increase(points []Point) (delta float64, ok bool) {
	if len(points) < 2 {
		return 0, false
	}
	for i := 1; i < len(points); i++ {
		if d := points[i].V - points[i-1].V; d > 0 {
			delta += d
		}
	}
	return delta, true
}

// RateOverWindow averages a cumulative series into a per-second rate across
// its own first-to-last span, using the same reset-safe Increase above. ok
// is false with fewer than two points or a zero/negative elapsed span (two
// points landing in the same millisecond would otherwise divide by zero).
func RateOverWindow(points []Point) (rate float64, ok bool) {
	delta, ok := Increase(points)
	if !ok {
		return 0, false
	}
	elapsed := float64(points[len(points)-1].T-points[0].T) / 1000
	if elapsed <= 0 {
		return 0, false
	}
	return delta / elapsed, true
}
