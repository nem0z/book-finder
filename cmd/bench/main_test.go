package main

import (
	"slices"
	"testing"
)

func TestParseDepths(t *testing.T) {
	if got, err := parseDepths("10, 20,30,100"); err != nil || !slices.Equal(got, []int{30, 10, 20, 100}) {
		t.Errorf("got %v, %v", got, err)
	}

	for _, bad := range []string{"0", "101", "ten", ""} {
		if _, err := parseDepths(bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}
