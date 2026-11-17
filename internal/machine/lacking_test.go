package machine

// The controllers crate.slice must have, read from its cgroup.controllers: cpu, io and memory.

import (
	"reflect"
	"testing"
)

func TestASliceLacksEachControllerItsGroupDoesNotHold(t *testing.T) {
	for _, row := range []struct {
		have, want []string
	}{
		{[]string{"cpuset", "cpu", "io", "memory", "pids"}, nil},
		{[]string{"cpu", "memory", "pids"}, []string{"io"}},
		{[]string{"pids"}, []string{"cpu", "io", "memory"}},
		{nil, []string{"cpu", "io", "memory"}},
	} {
		if got := lacking(row.have); !reflect.DeepEqual(got, row.want) {
			t.Errorf("%v: expected it to lack %v, got %v", row.have, row.want, got)
		}
	}
}
