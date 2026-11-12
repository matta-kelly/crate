package machine_test

// Where crate places nothing: behind a remote engine, or with no machine file, it places nothing, holds
// nothing, and no count applies.

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

// With no machine file, crate places nothing and holds nothing, and no count applies.
func TestWithNoMachineFileNothingIsPlacedOrHeld(t *testing.T) {
	f := newFixture(t, 1, 1, 1)
	systems := []string{f.system("a"), f.system("b"), f.system("c")}
	missing := filepath.Join(t.TempDir(), "never.toml")
	h := f.startHelper("none", machine.Work, f.system("d"),
		"CRATE_MACHINE="+missing, "HELPER_SYSTEMS="+strings.Join(systems, ","), scaleFast)
	noSlotIsHeld(t, f, h.waitOut(t, 30*time.Second))
	h.waitExit(t, 30*time.Second)
}
