package system_test

import (
	"errors"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

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

// A machine whose Hold takes longer than the limit: the slot it gives late is released, not kept.
type lateMachine struct {
	delay    time.Duration
	places   atomic.Int64
	released atomic.Int64
}

func (m *lateMachine) Hold(kind, system string) (machine.Slot, error) {
	time.Sleep(m.delay)
	return lateSlot{m}, nil
}

func (m *lateMachine) Place(kind, system string) (machine.Placement, error) {
	m.places.Add(1)
	return "", nil
}

type lateSlot struct{ m *lateMachine }

func (lateSlot) Bind()            {}
func (s lateSlot) Release()       { s.m.released.Add(1) }

// The wait is bounded by limit.Hold: past it Hold returns, holds nothing, makes nothing, and a slot that
// arrives late is released.
func TestHoldIsBoundedByItsLimit(t *testing.T) {
	// limit.Hold is one hour, so a scale of 3600 makes it one second.
	t.Setenv("CRATE_LIMIT_SCALE", strconv.Itoa(3600))
	m := &lateMachine{delay: 2 * time.Second}
	s := system.New(m, system.Spec{Kind: system.Work, Name: "que-pasa-work", Checkout: "/nonexistent"})

	start := time.Now()
	err := s.Hold()
	took := time.Since(start)
	if err == nil {
		t.Fatal("Hold with a machine slower than its limit: expected an error, got none")
	}
	if took < 800*time.Millisecond || took > 4*time.Second {
		t.Errorf("Hold: expected about the one second limit, took %v (error %v)", took, err)
	}
	if places := m.places.Load(); places != 0 {
		t.Errorf("Hold past its limit: expected no Place, got %d", places)
	}
	deadline := time.Now().Add(3 * time.Second)
	for m.released.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := m.released.Load(); got != 1 {
		t.Errorf("the slot that arrived after the limit: expected one Release, got %d", got)
	}
}
