package machinetest_test

import (
	"reflect"
	"testing"

	"github.com/matta-kelly/crate/internal/machine"
	"github.com/matta-kelly/crate/internal/machine/machinetest"
)

// A recorder with no slice gives none.
func TestARecorderGivesItsSliceAndListsTheCallsInOrder(t *testing.T) {
	r := &machinetest.Recorder{Slice: "crate-attempt-1.slice"}
	var m machine.Machine = r
	slot, err := m.Hold("attempt", "que-pasa-main")
	if err != nil || slot == nil {
		t.Fatalf("Hold: expected a slot, got %v, %v", slot, err)
	}
	placement, err := m.Place("attempt", "que-pasa-main")
	if err != nil || placement != "crate-attempt-1.slice" {
		t.Errorf("Place: expected the slice a test names, got %q, %v", placement, err)
	}
	slot.Bind()
	slot.Release()
	if _, err := m.Place("work", "build"); err != nil {
		t.Fatalf("Place work: %v", err)
	}
	none := &machinetest.Recorder{}
	if p, err := none.Place("check", "s"); err != nil || p != "" {
		t.Errorf("a recorder with no slice: expected no placement and no error, got %q, %v", p, err)
	}
	want := []machinetest.Call{
		{Op: "Hold", Kind: "attempt", System: "que-pasa-main"},
		{Op: "Place", Kind: "attempt", System: "que-pasa-main"},
		{Op: "Bind", Kind: "attempt", System: "que-pasa-main"},
		{Op: "Release", Kind: "attempt", System: "que-pasa-main"},
		{Op: "Place", Kind: "work", System: "build"},
	}
	if !reflect.DeepEqual(r.Calls, want) {
		t.Errorf("the calls: expected\n%+v\ngot\n%+v", want, r.Calls)
	}
}

func TestARecorderRefusesAKindOutsideTheFour(t *testing.T) {
	r := &machinetest.Recorder{Slice: "x.slice"}
	if _, err := r.Hold("bogus", "s"); err == nil {
		t.Error("Hold: expected an error for a kind outside the four, got none")
	}
	if _, err := r.Place("bogus", "s"); err == nil {
		t.Error("Place: expected an error for a kind outside the four, got none")
	}
}
