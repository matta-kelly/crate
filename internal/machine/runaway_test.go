package machine_test

// The runaway: a member of one system that takes more memory than the machine has is killed, its neighbour
// runs on, and the machine's slice stays.

import (
	"testing"
	"time"

	"github.com/matta-kelly/crate/internal/machine"
)

func TestARunawayDiesAlone(t *testing.T) {
	f := newFixture(t, "0-1", "512M", 4, 4, 4)
	neighbour, _ := f.start(machine.Check, f.system("neighbour"), nil, []string{"sleep", "120"})
	time.Sleep(time.Second)
	if !f.running(neighbour) {
		t.Fatalf("the neighbour: expected it to start before the runaway, got stopped")
	}
	runaway, _ := f.start(machine.Work, f.system("runaway"), nil,
		[]string{"sh", "-c", "exec awk 'BEGIN{for(i=0;;i++) a[i]=i}'"})

	deadline := time.Now().Add(45 * time.Second)
	for {
		if f.run("podman", "inspect", "-f", "{{.State.Status}}", runaway) == "exited" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the runaway: expected it to be killed within 45s, it still runs")
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !f.oomKilled(runaway) {
		t.Errorf("the runaway: expected OOMKilled=true, got false")
	}
	if !f.running(neighbour) {
		t.Errorf("the neighbour: expected it to run on, got stopped")
	}
	if state := f.show(f.slice+".slice", "LoadState")["LoadState"]; state != "loaded" {
		t.Errorf("the machine's slice: expected it to stay loaded, got %q", state)
	}
}
