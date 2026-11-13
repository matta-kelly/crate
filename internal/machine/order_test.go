package machine_test

// The order: each kind goes in a slice of its own under the machine's slice, with a weight that puts work
// first, a check next, postsubmit and attempt last, and crate sets no CPU or memory limit of its own.

import (
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matta-kelly/crate/internal/machine"
)

var theFour = []string{machine.Work, machine.Check, machine.Postsubmit, machine.Attempt}

// Place gives <slice>-<kind>-<system>.slice under <slice>-<kind>.slice under <slice>.slice, each kind's
// slice with a weight in the contract's order, and no CPU or memory limit on either level.
func TestPlaceNamesEachSliceUnderItsKindAndTheMachinesSlice(t *testing.T) {
	f := newFixture(t, 1, 1, 1)
	weights := map[string]int{}
	for _, kind := range theFour {
		leaf := f.place(kind, "que-pasa-main")
		if want := f.slice + "-" + kind + `-que\x2dpasa\x2dmain.slice`; string(leaf) != want {
			t.Errorf("Place(%q, %q): expected %q, got %q", kind, "que-pasa-main", want, leaf)
		}
		kindSlice := f.slice + "-" + kind + ".slice"
		if got := f.show(string(leaf), "Slice")["Slice"]; got != kindSlice {
			t.Errorf("the %s leaf's slice: expected %q, got %q", kind, kindSlice, got)
		}
		if got := f.show(kindSlice, "Slice")["Slice"]; got != f.slice+".slice" {
			t.Errorf("the %s kind's slice: expected it under %q, got %q", kind, f.slice+".slice", got)
		}
		weight, err := strconv.Atoi(f.show(kindSlice, "CPUWeight")["CPUWeight"])
		if err != nil || weight < 1 {
			t.Errorf("the %s kind's slice: expected a CPUWeight, got %q", kind, f.show(kindSlice, "CPUWeight")["CPUWeight"])
		}
		weights[kind] = weight
		for _, unit := range []string{f.slice + ".slice", kindSlice} {
			props := f.show(unit, "AllowedCPUs", "MemoryMax", "MemorySwapMax", "CPUQuotaPerSecUSec")
			for key, value := range props {
				if !noLimit(value) {
					t.Errorf("%s: expected no %s of crate's, got %q", unit, key, value)
				}
			}
		}
	}
	if !(weights[machine.Work] > weights[machine.Check]) {
		t.Errorf("the weights: expected work above check, got work %d, check %d", weights[machine.Work], weights[machine.Check])
	}
	if !(weights[machine.Check] > weights[machine.Postsubmit]) {
		t.Errorf("the weights: expected check above postsubmit, got check %d, postsubmit %d", weights[machine.Check], weights[machine.Postsubmit])
	}
	if !(weights[machine.Check] > weights[machine.Attempt]) {
		t.Errorf("the weights: expected check above attempt, got check %d, attempt %d", weights[machine.Check], weights[machine.Attempt])
	}
}

// Unplace stops the system's own slice, so a removed system leaves none; its kind's slice stays, and a second
// Unplace, of a slice already gone, is no error.
func TestUnplaceStopsTheSystemsSliceAndKeepsItsKinds(t *testing.T) {
	f := newFixture(t, 1, 1, 1)
	leaf := string(f.place(machine.Work, "que-pasa-main"))
	kindSlice := f.slice + "-" + machine.Work + ".slice"
	for i := 0; i < 2; i++ {
		h := f.startHelper("unplace", machine.Work, "que-pasa-main")
		if out := strings.TrimSpace(h.waitOut(t, 30*time.Second)); out != "ok" {
			t.Fatalf("Unplace %d: expected ok, got %q", i+1, out)
		}
		h.waitExit(t, 30*time.Second)
	}
	if got := f.show(leaf, "ActiveState")["ActiveState"]; got == "active" {
		t.Errorf("the leaf %s after Unplace: expected it stopped, got %s", leaf, got)
	}
	if got := f.show(kindSlice, "ActiveState")["ActiveState"]; got != "active" {
		t.Errorf("the kind's slice %s after Unplace: expected it kept, got %s", kindSlice, got)
	}
}

// noLimit is what systemd shows for a property crate does not set.
func noLimit(value string) bool {
	switch value {
	case "", "[not set]", "infinity", "max":
		return true
	}
	return false
}

// Four containers, one of each kind, each burning more threads than the machine has CPUs: each runs in its
// kind's slice, and the CPU time used is work's above the check's, and the check's above the other two.
func TestTheKindsShareTheCPUsInOrder(t *testing.T) {
	f := newFixture(t, 4, 4, 4)
	threads := runtime.NumCPU()*2 + 1
	names := map[string]string{}
	leaves := map[string]machine.Placement{}
	for _, kind := range theFour {
		name, leaf := f.start(kind, f.plainSystem(kind), nil, burn(threads))
		names[kind], leaves[kind] = name, leaf
	}
	for _, kind := range theFour {
		if cg := f.cgroup(names[kind]); !strings.Contains(cg, "/"+string(leaves[kind])+"/") {
			t.Errorf("the %s container's cgroup: expected it under %q, got %q", kind, leaves[kind], cg)
		}
	}
	deadline := time.Now().Add(30 * time.Second)
	for _, kind := range theFour {
		for !f.running(names[kind]) {
			if time.Now().After(deadline) {
				t.Fatalf("the %s container: expected it to run, got stopped", kind)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	time.Sleep(time.Second)
	before := map[string]int64{}
	for kind, name := range names {
		before[kind] = f.cpu(name)
	}
	time.Sleep(5 * time.Second)
	used := map[string]int64{}
	for kind, name := range names {
		used[kind] = f.cpu(name) - before[kind]
	}
	if !(used[machine.Work] > used[machine.Check]) {
		t.Errorf("work and check: expected work's CPU time greater, got work %d us, check %d us", used[machine.Work], used[machine.Check])
	}
	if !(used[machine.Check] > used[machine.Postsubmit]) {
		t.Errorf("check and postsubmit: expected check's CPU time greater, got check %d us, postsubmit %d us", used[machine.Check], used[machine.Postsubmit])
	}
	if !(used[machine.Check] > used[machine.Attempt]) {
		t.Errorf("check and attempt: expected check's CPU time greater, got check %d us, attempt %d us", used[machine.Check], used[machine.Attempt])
	}
}
