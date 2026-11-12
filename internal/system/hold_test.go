package system_test

import (
	"errors"
	"testing"

	"github.com/matta-kelly/crate/internal/machine"
	"github.com/matta-kelly/crate/internal/system"
)

// A machine whose Hold never gives a slot: past the limit it returns its error.
type noSlotMachine struct {
	err    error
	places int
}

func (m *noSlotMachine) Hold(kind, system string) (machine.Slot, error) { return nil, m.err }
func (m *noSlotMachine) Place(kind, system string) (machine.Placement, error) {
	m.places++
	return "", nil
}

// Past its limit Hold returns the machine's error, holds nothing, and the caller makes nothing.
func TestHoldReturnsTheMachinesErrorAndMakesNothing(t *testing.T) {
	want := errors.New("no slot for an hour")
	m := &noSlotMachine{err: want}
	s := system.New(m, system.Spec{Kind: system.Work, Name: "que-pasa-work", Checkout: "/nonexistent"})
	err := s.Hold()
	if !errors.Is(err, want) {
		t.Errorf("Hold: expected the machine's error, got %v", err)
	}
	if m.places != 0 {
		t.Errorf("Hold past its limit: expected no Place, got %d", m.places)
	}
}
