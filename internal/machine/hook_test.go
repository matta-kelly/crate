package machine_test

// From a hook: with no XDG_RUNTIME_DIR and no DBUS_SESSION_BUS_ADDRESS, Place still reaches the user's
// manager at /run/user/<uid>.

import (
	"testing"
	"time"

	"github.com/matta-kelly/crate/internal/machine"
)

func TestPlaceReachesTheUsersManagerFromAHookEnvironment(t *testing.T) {
	f := newFixture(t, 1, 1, 1)
	h := f.hookHelper("place", machine.Work, "router")
	out := h.waitOut(t, 30*time.Second)
	h.waitExit(t, 30*time.Second)
	if want := "ok " + f.slice + "-work-router.slice"; out != want {
		t.Errorf("Place with no XDG_RUNTIME_DIR and no DBUS_SESSION_BUS_ADDRESS: expected %q, got %q", want, out)
	}
}
