package event

import (
	"math"
	"testing"
)

func TestIncSeqID(t *testing.T) {
	tests := []struct {
		name string
		in   uint64
		want uint64
	}{
		{name: "zero", in: 0, want: 1},
		{name: "one", in: 1, want: 2},
		{name: "normal", in: 100, want: 101},
		{name: "max wraps to 1", in: math.MaxUint64, want: 1},
		{name: "just below max", in: math.MaxUint64 - 1, want: math.MaxUint64},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := incSeqID(tt.in); got != tt.want {
				t.Errorf("incSeqID(%d) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}
