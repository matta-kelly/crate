package machine_test

// Where crate places nothing: behind a remote engine, whose work runs on another machine, it places nothing, holds
// nothing, and no count applies. A machine with no file is not one: it places, and counts one of each kind.

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matta-kelly/crate/internal/machine"
)

// noSlotIsHeld says the machine placed nothing and held nothing, and that no kind kept a slot.
func noSlotIsHeld(t *testing.T, f *fixture, out string) {
	t.Helper()
	took, placement := noneOut(t, out)
	if placement != "" {
		t.Errorf("Place: expected no placement, got %q", placement)
	}
	if took > 2000 {
		t.Errorf("three Holds with no count: expected no wait, took %d ms", took)
	}
	for _, kind := range theFour {
		if files := f.slotFiles(kind); len(files) != 0 {
			t.Errorf("the %s slots: expected none, got %v", kind, files)
		}
	}
}

// With CONTAINER_HOST, the engine is remote: crate places nothing and holds nothing.
func TestWithARemoteEngineNothingIsPlacedOrHeld(t *testing.T) {
	f := newFixture(t, 1, 1, 1)
	systems := []string{f.system("a"), f.system("b"), f.system("c")}
	h := f.startHelper("none", machine.Work, f.system("d"),
		"CONTAINER_HOST=unix:///run/user/1000/podman/podman.sock",
		"HELPER_SYSTEMS="+strings.Join(systems, ","), scaleFast)
	noSlotIsHeld(t, f, h.waitOut(t, 30*time.Second))
	h.waitExit(t, 30*time.Second)
}

// With no machine file, crate still places its work under its own slice, and counts one of each kind at once: an
// untuned machine is bounded too.
func TestWithNoMachineFileCratePlacesAndCountsOne(t *testing.T) {
	f := newFixture(t, 1, 1, 1)
	missing := "CRATE_MACHINE=" + filepath.Join(t.TempDir(), "never.toml")
	leaf := f.place(machine.Attempt, "que-pasa-main", missing)
	if want := f.slice + "-" + machine.Attempt + `-que\x2dpasa\x2dmain.slice`; string(leaf) != want {
		t.Errorf("Place with no file: expected %q, got %q", want, leaf)
	}
	first := f.startHelper("hold", machine.Attempt, f.system("a"), missing, "HELPER_RELEASE="+f.absent())
	requireHeld(t, first.waitOut(t, 30*time.Second), "the first holder")
	second := f.startHelper("hold", machine.Attempt, f.system("b"), missing, scaleFast)
	out := second.waitOut(t, 30*time.Second)
	if !strings.Contains(out, "count 1") {
		t.Errorf("a second attempt with no file: expected it to wait on a count of 1, got %q", out)
	}
}
