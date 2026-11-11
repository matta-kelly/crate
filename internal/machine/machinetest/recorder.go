// Package machinetest holds a Machine for tests. A stub: da-hammer builds it.
package machinetest

import "github.com/matta-kelly/crate/internal/machine"

// Call is one call on a Recorder: Op is Hold, Bind, Release or Place.
type Call struct {
	Op     string
	Kind   string
	System string
}

// Recorder is a Machine that records each call in order and places in Slice ("" for none).
type Recorder struct {
	Slice string
	Calls []Call
}

var _ machine.Machine = (*Recorder)(nil)

func (r *Recorder) Hold(kind, system string) (machine.Slot, error)       { return nil, nil }
func (r *Recorder) Place(kind, system string) (machine.Placement, error) { return "", nil }
