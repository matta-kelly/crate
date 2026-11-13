// Package machinetest holds a Machine for tests: it records each call and places in the slice a test names.
package machinetest

import (
	"sync"

	"github.com/matta-kelly/crate/internal/machine"
)

// Call is one call on a Recorder: Op is Hold, Bind, Release, Place or Unplace.
type Call struct {
	Op     string
	Kind   string
	System string
}

// Recorder is a Machine that records each call in order and places in Slice ("" for none).
type Recorder struct {
	Slice string
	Calls []Call

	mu sync.Mutex
}

var _ machine.Machine = (*Recorder)(nil)

func (r *Recorder) record(op, kind, system string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Calls = append(r.Calls, Call{Op: op, Kind: kind, System: system})
}

func (r *Recorder) Hold(kind, system string) (machine.Slot, error) {
	if err := machine.CheckKind(kind); err != nil {
		return nil, err
	}
	r.record("Hold", kind, system)
	return &slot{r: r, kind: kind, system: system}, nil
}

func (r *Recorder) Place(kind, system string) (machine.Placement, error) {
	if err := machine.CheckKind(kind); err != nil {
		return "", err
	}
	r.record("Place", kind, system)
	return machine.Placement(r.Slice), nil
}

func (r *Recorder) Unplace(kind, system string) error {
	if err := machine.CheckKind(kind); err != nil {
		return err
	}
	r.record("Unplace", kind, system)
	return nil
}

type slot struct {
	r            *Recorder
	kind, system string
}

func (s *slot) Bind()    { s.r.record("Bind", s.kind, s.system) }
func (s *slot) Release() { s.r.record("Release", s.kind, s.system) }
