package machine_test

// The ceiling, the fill and the split: one slice of the file's CPUs and memory, which one member alone can
// fill, and which its kinds share in order.

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matta-kelly/crate/internal/machine"
)

func sumUsage(f *fixture, names []string) int64 {
	var total int64
	for _, name := range names {
		total += f.cpu(name)
	}
	return total
}

// Four kinds burn more threads than the set, and the machine's slice stays within its two CPUs and 512M.
func TestTheMachinesSliceHoldsTheCeiling(t *testing.T) {
	f := newFixture(t, "0-1", "512M", 4, 4, 4)
	var names []string
	for i, kind := range []string{machine.Work, machine.Check, machine.Postsubmit, machine.Attempt} {
		name, _ := f.start(kind, f.system(strconv.Itoa(i)), nil, burn())
		names = append(names, name)
	}
	time.Sleep(time.Second)
	started := time.Now()
	before := sumUsage(f, names)
	time.Sleep(5 * time.Second)
	used := sumUsage(f, names) - before
	// two CPUs over the window is its upper bound; the window is the wall it actually took, since a loaded
	// machine can make the sleep longer than five seconds.
	wall := int64(time.Since(started) / time.Microsecond)
	if limit := wall*2 + wall/10 + 100_000; used > limit {
		t.Errorf("the slice's CPU time over %d us: expected at most two CPUs (%d us with slack), got %d us", wall, limit, used)
	}
	peak, err := strconv.ParseInt(strings.TrimSpace(readFile(t, f.base()+"/memory.peak")), 10, 64)
	if err != nil {
		t.Fatalf("memory.peak: %v", err)
	}
	if peak > 512*1024*1024 {
		t.Errorf("the slice's memory peak: expected at most 512M, got %d", peak)
	}
}

// One member alone takes at least nine tenths of the set, and has no cap of its own.
func TestOneMemberAloneTakesTheWholeCeiling(t *testing.T) {
	f := newFixture(t, "0-1", "512M", 4, 4, 4)
	name, _ := f.start(machine.Work, f.system("a"), nil, burn())
	time.Sleep(time.Second)
	started := time.Now()
	before := f.cpu(name)
	time.Sleep(5 * time.Second)
	used := f.cpu(name) - before
	wall := int64(time.Since(started) / time.Microsecond)
	if used < wall*2*9/10 {
		t.Errorf("one member alone over %d us: expected at least nine tenths of two CPUs (%d us), got %d us", wall, wall*2*9/10, used)
	}
	if limit := wall*2 + wall/10 + 100_000; used > limit {
		t.Errorf("one member alone over %d us: expected at most two CPUs (%d us with slack), got %d us", wall, limit, used)
	}
	cg := f.cgroup(name)
	if max := cgroupField(t, cg, "cpu.max"); max != "" {
		if fields := strings.Fields(max); len(fields) > 0 && fields[0] != "max" {
			t.Errorf("the member's own cpu.max: expected no quota, got %q", max)
		}
	}
	if max := cgroupField(t, cg, "memory.max"); max != "" && max != "max" {
		t.Errorf("the member's own memory.max: expected no cap, got %q", max)
	}
}

// Four kinds each burn more threads than the set: a person's work first, a check next, postsubmit and
// attempt last.
func TestTheKindsShareTheCeilingInOrder(t *testing.T) {
	f := newFixture(t, "0-1", "512M", 4, 4, 4)
	names := map[string]string{}
	for _, kind := range []string{machine.Work, machine.Check, machine.Postsubmit, machine.Attempt} {
		name, _ := f.start(kind, f.system(kind), nil, burn())
		names[kind] = name
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
