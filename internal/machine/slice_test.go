package machine_test

// The slices promise: every container goes in a slice of the machine file's CPUs and memory, by kind, under
// the slice the machine names.

import (
	"strings"
	"testing"

	"github.com/matta-kelly/crate/internal/machine"
)

// A system's name is escaped as systemd-escape does, since a - in a slice name is a level.
func TestPlaceNamesEverySliceUnderTheMachinesSlice(t *testing.T) {
	f := newFixture(t, "0-1", "512M", 1, 1, 1)
	rows := []struct {
		kind, system, want string
	}{
		{machine.Check, "que-pasa-main", f.slice + "-check-que\\x2dpasa\\x2dmain.slice"},
		{machine.Work, "router", f.slice + "-work-router.slice"},
	}
	for _, row := range rows {
		if got := string(f.place(row.kind, row.system)); got != row.want {
			t.Errorf("Place(%q, %q): expected %q, got %q", row.kind, row.system, row.want, got)
		}
	}
}

// The machine's slice takes the file's ceiling, and each kind's slice takes a weight.
func TestPlaceSetsTheCeilingAndAWeightOnEachKind(t *testing.T) {
	f := newFixture(t, "0-1", "512M", 1, 1, 1)
	for _, kind := range []string{machine.Work, machine.Check, machine.Postsubmit, machine.Attempt} {
		leaf := f.place(kind, f.system(kind))
		props := f.show(string(leaf), "Slice")
		if want := f.slice + "-" + kind + ".slice"; props["Slice"] != want {
			t.Errorf("Place(%q): expected its slice %q, got %q", kind, want, props["Slice"])
		}
		weight := f.show(f.slice+"-"+kind+".slice", "CPUWeight")["CPUWeight"]
		if weight == "" || weight == "[not set]" || weight == "0" {
			t.Errorf("the %s kind's slice: expected a CPUWeight, got %q", kind, weight)
		}
	}
	base := f.show(f.slice+".slice", "AllowedCPUs", "MemoryMax", "MemorySwapMax")
	want := map[string]string{"AllowedCPUs": "0-1", "MemoryMax": "536870912", "MemorySwapMax": "0"}
	for key, value := range want {
		if base[key] != value {
			t.Errorf("the machine's slice: expected %s=%s, got %q", key, value, base[key])
		}
	}
}

// Placing again gives the same slice and does not restart a member that already runs there.
func TestPlacingAgainKeepsARunningMember(t *testing.T) {
	f := newFixture(t, "0-1", "512M", 1, 1, 1)
	name, leaf := f.start(machine.Check, f.system("a"), nil, []string{"sleep", "60"})
	before := f.cgroup(name)
	again := f.place(machine.Check, f.system("a"))
	if again != leaf {
		t.Errorf("the same kind and system: expected %q again, got %q", leaf, again)
	}
	f.place(machine.Work, f.system("b"))
	if !f.running(name) {
		t.Errorf("the member: expected it to run on after a second Place, got stopped")
	}
	if after := f.cgroup(name); after != before {
		t.Errorf("the member's cgroup: expected %q to stay, got %q", before, after)
	}
}

// A container started with --cgroup-parent= the placement has its /proc/<pid>/cgroup under that slice.
func TestAContainerWithThePlacementIsUnderItsSlice(t *testing.T) {
	f := newFixture(t, "0-1", "512M", 1, 1, 1)
	name, leaf := f.start(machine.Work, f.system("a"), nil, []string{"sleep", "60"})
	cg := f.cgroup(name)
	if !strings.Contains(cg, "/"+string(leaf)+"/") {
		t.Errorf("the container's cgroup: expected it under %q, got %q", leaf, cg)
	}
	if !strings.HasSuffix(cg, "/container") {
		t.Errorf("the container's cgroup: expected the container's own folder, got %q", cg)
	}
}
