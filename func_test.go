package event

import (
	"reflect"
	"testing"
)

func Test_incSeqID(t *testing.T) {
	type args struct {
		seq uint64
	}
	tests := []struct {
		name string
		args func(t *testing.T) args

		want1 uint64
	}{
		//TODO: Add test cases
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tArgs := tt.args(t)

			got1 := incSeqID(tArgs.seq)

			if !reflect.DeepEqual(got1, tt.want1) {
				t.Errorf("incSeqID got1 = %v, want1: %v", got1, tt.want1)
			}
		})
	}
}
