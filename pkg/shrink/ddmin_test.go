package shrink

import (
	"slices"
	"testing"
)

// predicate returns a BatchTest that fails when the subset contains all of need, counting calls.
func predicate(need []int, calls *int) BatchTest[int] {
	return func(cands [][]int) []bool {
		out := make([]bool, len(cands))
		for i, c := range cands {
			*calls++
			out[i] = true
			for _, n := range need {
				if !slices.Contains(c, n) {
					out[i] = false
				}
			}
		}
		return out
	}
}

func TestDDMin(t *testing.T) {
	items := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	cases := []struct {
		name string
		need []int
		want []int
	}{
		{"single culprit", []int{3}, []int{3}},
		{"pair", []int{1, 6}, []int{1, 6}},
		{"adjacent pair", []int{4, 5}, []int{4, 5}},
		{"triple", []int{0, 5, 9}, []int{0, 5, 9}},
		{"none needed", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			got := DDMin(items, predicate(tc.need, &calls))
			if !slices.Equal(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
			if calls > 100 { // uncached; the shrinker caches trials
				t.Fatalf("%d tests", calls)
			}
		})
	}
}

func TestDDMinSmallInputs(t *testing.T) {
	calls := 0
	if got := DDMin([]int{7}, predicate([]int{7}, &calls)); !slices.Equal(got, []int{7}) {
		t.Fatal(got)
	}
	if got := DDMin([]int{}, predicate(nil, &calls)); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestSplit(t *testing.T) {
	got := split([]int{1, 2, 3, 4, 5}, 3)
	if len(got) != 3 || len(got[0])+len(got[1])+len(got[2]) != 5 {
		t.Fatal(got)
	}
	if c := complement(got, 1); len(c) != 5-len(got[1]) {
		t.Fatal(c)
	}
}
