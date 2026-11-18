// Package shrink minimizes failing runs: it finds a small fault schedule that
// still makes the scenario FAIL (delta debugging over faults.json, then shorter
// fault durations).
package shrink

// BatchTest evaluates candidate subsets and reports, per candidate, whether it
// still fails. Implementations may run candidates in parallel; results must be
// returned in the same order as the candidates.
type BatchTest[T any] func(cands [][]T) []bool

// DDMin returns a 1-minimal subset of items for which test still fails, using
// Zeller's delta debugging. It assumes test(items) fails. Within each round,
// candidates are tested as one batch and the first failing one (in order) wins,
// so the result does not depend on how batches are scheduled.
func DDMin[T any](items []T, test BatchTest[T]) []T {
	c := append([]T(nil), items...)
	n := 2
	for len(c) >= 2 {
		chunks := split(c, n)
		if i := firstFailing(chunks, test); i >= 0 {
			c, n = chunks[i], 2
			continue
		}
		if n > 2 {
			comps := make([][]T, len(chunks))
			for i := range chunks {
				comps[i] = complement(chunks, i)
			}
			if i := firstFailing(comps, test); i >= 0 {
				c, n = comps[i], max(n-1, 2)
				continue
			}
		}
		if n >= len(c) {
			break
		}
		n = min(2*n, len(c))
	}
	if len(c) == 1 {
		if r := test([][]T{{}}); len(r) == 1 && r[0] {
			return nil
		}
	}
	return c
}

func firstFailing[T any](cands [][]T, test BatchTest[T]) int {
	for i, failed := range test(cands) {
		if failed {
			return i
		}
	}
	return -1
}

// split divides c into n nearly equal, contiguous, non-empty chunks.
func split[T any](c []T, n int) [][]T {
	out := make([][]T, 0, n)
	start := 0
	for i := 0; i < n; i++ {
		end := start + (len(c)-start)/(n-i)
		out = append(out, c[start:end])
		start = end
	}
	return out
}

// complement returns all chunks except chunk i, concatenated in order.
func complement[T any](chunks [][]T, i int) []T {
	var out []T
	for j, ch := range chunks {
		if j != i {
			out = append(out, ch...)
		}
	}
	return out
}
