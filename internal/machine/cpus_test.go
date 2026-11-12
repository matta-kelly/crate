package machine_test

// CPUs the machine does not have: a valid list places, a list with a CPU outside /sys says so before systemd
// is asked, and a /sys that cannot be read is a failure that names it.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matta-kelly/crate/internal/machine"
)

// A list the machine has places, as every other placement test shows; this is the valid row of the case.
func TestPlaceAcceptsACPUListTheMachineHas(t *testing.T) {
	f := newFixture(t, "0", "512M", 1, 1, 1)
	if got := string(f.place(machine.Work, f.system("a"))); got == "" {
		t.Errorf("Place with a CPU the machine has: expected a slice, got none")
	}
}

// A CPU outside /sys/devices/system/cpu/possible is refused with the file, the key and the CPUs the machine
// has, before any systemd call.
func TestPlaceRefusesACPUTheMachineDoesNotHave(t *testing.T) {
	f := newFixture(t, "999999", "512M", 1, 1, 1)
	log := filepath.Join(t.TempDir(), "calls")
	f.pathWith(map[string]string{
		"systemctl":   "#!/bin/sh\necho \"systemctl $*\" >> " + log + "\nexit 0\n",
		"systemd-run": "#!/bin/sh\necho \"systemd-run $*\" >> " + log + "\nexit 0\n",
	})
	placement, err := machine.Default().Place(machine.Work, f.system("a"))
	if err == nil {
		t.Fatalf("Place with a CPU the machine does not have: expected a refusal, got %q and no error", placement)
	}
	possible := strings.TrimSpace(readFile(t, "/sys/devices/system/cpu/possible"))
	for _, want := range []string{f.machine, "cpus", possible} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal: expected it to name %q, got %q", want, err)
		}
	}
	if data, _ := os.ReadFile(log); len(data) != 0 {
		t.Errorf("the refusal: expected no systemd call before the file's CPUs are checked, got\n%s", data)
	}
}

// A /sys the process cannot read is a failure that names it.
func TestPlaceFailsWhenItCannotReadTheCPUList(t *testing.T) {
	f := newFixture(t, "0", "512M", 1, 1, 1)
	h := f.startHelperNoSys("place", machine.Work, f.system("a"), "CRATE_LIMIT_SCALE=30")
	out := h.waitOut(t, 15*time.Second)
	message, ok := strings.CutPrefix(out, "err ")
	if !ok {
		t.Fatalf("Place with no /sys: expected an error, got %q", out)
	}
	if !strings.Contains(message, "/sys/devices/system/cpu/possible") {
		t.Errorf("the refusal: expected it to name /sys/devices/system/cpu/possible, got %q", message)
	}
}
