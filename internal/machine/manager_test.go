package machine_test

// The manager: the user's systemd, from a hook's environment or with no manager at all; a remote engine;
// and a manager with no cpuset.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matta-kelly/crate/internal/machine"
)

// A process a git hook starts has no XDG_RUNTIME_DIR and no session bus: Place still reaches /run/user/<uid>.
func TestPlaceReachesTheUsersManagerFromAHookEnvironment(t *testing.T) {
	f := newFixture(t, "0-1", "512M", 1, 1, 1)
	h := f.startHelperBare("place", machine.Work, "router")
	out := h.waitOut(t, 15*time.Second)
	if want := "ok " + f.slice + "-work-router.slice"; out != want {
		t.Errorf("Place with no XDG_RUNTIME_DIR and no DBUS_SESSION_BUS_ADDRESS: expected %q, got %q", want, out)
	}
}

// With no running user manager, Place refuses and says the user needs linger.
func TestPlaceRefusesWhenThereIsNoUserManager(t *testing.T) {
	f := newFixture(t, "0-1", "512M", 1, 1, 1)
	empty := t.TempDir()
	h := f.startHelperBare("place", machine.Work, "router",
		"XDG_RUNTIME_DIR="+empty,
		"DBUS_SESSION_BUS_ADDRESS=unix:path="+empty+"/bus")
	out := h.waitOut(t, 15*time.Second)
	message, ok := strings.CutPrefix(out, "err ")
	if !ok {
		t.Fatalf("Place with no running manager: expected an error, got %q", out)
	}
	if !strings.Contains(strings.ToLower(message), "linger") {
		t.Errorf("Place with no running manager: expected it to say the user needs linger, got %q", message)
	}
}

// A remote engine has no slice crate can set up: Place gives none and Hold holds nothing, whatever the file says.
func TestARemoteEnginePlacesNothingAndHoldsNothing(t *testing.T) {
	f := newFixture(t, "0-1", "512M", 1, 1, 1)
	local := f.place(machine.Work, "router")
	if local == "" {
		t.Fatalf("Place on this machine: expected a slice")
	}

	t.Setenv("CONTAINER_HOST", "unix:///run/user/1000/podman/podman.sock")
	placement, err := machine.Default().Place(machine.Work, "router-remote")
	if err != nil || placement != "" {
		t.Errorf("Place with CONTAINER_HOST: expected no placement and no error, got %q, %v", placement, err)
	}
	if _, err := machine.Default().Hold(machine.Check, "router-remote"); err != nil {
		t.Errorf("Hold with CONTAINER_HOST: expected no error, got %v", err)
	}
	if files := f.slotFiles(machine.Check); len(files) != 0 {
		t.Errorf("Hold with CONTAINER_HOST: expected no slot, got %v", files)
	}
}

// faithfulManager is a stand-in systemctl: it logs each call and fails set-property with the message systemd
// gives on this target. cpuset is read from the user manager's cgroup.controllers, not through systemctl.
func faithfulManager(log string) string {
	return fmt.Sprintf(`#!/bin/sh
echo "$*" >> %q
case "$*" in
*set-property*) echo "Failed to set unit properties on the slice: Invalid argument" >&2; exit 1 ;;
esac
exit 0
`, log)
}

// With no cpuset in the user's manager's cgroup.controllers, Place refuses and says why and how to delegate
// it.
func TestPlaceRefusesWhenTheManagerHasNoCpuset(t *testing.T) {
	f := newFixture(t, "0", "512M", 1, 1, 1)
	log := filepath.Join(t.TempDir(), "calls")
	manager := faithfulManager(log)
	f.pathWith(map[string]string{"systemctl": manager, "systemd-run": manager})
	h := f.startHelperFakeCgroup("place", machine.Work, f.system("a"), "cpu memory pids")
	out := h.waitOut(t, 15*time.Second)
	message, ok := strings.CutPrefix(out, "err ")
	if !ok {
		t.Fatalf("Place with a manager that has no cpuset: expected a refusal, got %q", out)
	}
	lower := strings.ToLower(message)
	if !strings.Contains(lower, "cpuset") {
		t.Errorf("the refusal: expected it to say why (cpuset), got %q", message)
	}
	if !strings.Contains(lower, "delegate") {
		t.Errorf("the refusal: expected it to say how to delegate cpuset, got %q", message)
	}
	if data, _ := os.ReadFile(log); strings.Contains(string(data), "systemd-run") {
		t.Errorf("the refusal: expected no member started, got\n%s", data)
	}
}

// A set-property that systemd refuses makes Place return an error that names the unit, the property and
// systemd's message, starts no member, and adds no cpuset advice when cpuset is delegated.
func TestAFailedSystemctlAskNamesTheUnitThePropertyAndTheMessage(t *testing.T) {
	f := newFixture(t, "0", "512M", 1, 1, 1)
	log := filepath.Join(t.TempDir(), "calls")
	manager := faithfulManager(log)
	f.pathWith(map[string]string{"systemctl": manager, "systemd-run": manager})
	placement, err := machine.Default().Place(machine.Work, f.system("a"))
	if err == nil {
		t.Fatalf("Place with a manager that refuses set-property: expected an error, got none")
	}
	if placement != "" {
		t.Errorf("the failed ask: expected no placement, got %q", placement)
	}
	message := err.Error()
	for _, want := range []string{f.slice + ".slice", "AllowedCPUs", "Invalid argument"} {
		if !strings.Contains(message, want) {
			t.Errorf("the error: expected it to name %q, got %q", want, message)
		}
	}
	lower := strings.ToLower(message)
	if strings.Contains(lower, "cpuset") || strings.Contains(lower, "delegate") {
		t.Errorf("the error: expected no cpuset advice while cpuset is delegated, got %q", message)
	}
	if data, _ := os.ReadFile(log); strings.Contains(string(data), "systemd-run") {
		t.Errorf("the failed ask: expected no member started, got\n%s", data)
	}
}

// A podman answer that is not a clear yes or no counts a bound slot as held, never as free, and says so on
// stderr: a second Hold waits.
func TestAPodmanAnswerThatIsNotYesOrNoKeepsTheSlotHeld(t *testing.T) {
	f := newFixture(t, "0-1", "512M", 1, 1, 1)
	f.pathWith(map[string]string{"podman": `#!/bin/sh
case "$1 $2" in
"network create") exit 0 ;;
"network exists") echo "Error: cannot connect to the engine" >&2; exit 125 ;;
"network rm") exit 0 ;;
esac
exit 0
`})
	first, second := f.system("a"), f.system("b")
	h1 := f.startHelper("hold-bind-exit", machine.Attempt, first, "HELPER_NETWORK="+first)
	h1.waitOut(t, 10*time.Second)
	h1.waitExit(t, 10*time.Second)
	f.oneSlot(machine.Attempt)

	started := time.Now()
	h2 := f.startHelper("hold", machine.Attempt, second,
		"HELPER_RELEASE="+noFile(t), "CRATE_LIMIT_SCALE=3600")
	out := h2.waitOut(t, 15*time.Second)
	took := time.Since(started)
	message, ok := strings.CutPrefix(out, "err ")
	if !ok {
		t.Fatalf("the second Hold: expected it to wait and fail past the limit, got %q", out)
	}
	for _, want := range []string{machine.Attempt, "1", first} {
		if !strings.Contains(message, want) {
			t.Errorf("the wait's error: expected it to name %q, got %q", want, message)
		}
	}
	if took < 500*time.Millisecond {
		t.Errorf("the second Hold: expected it to wait when podman cannot answer, took %v", took)
	}
	if !strings.Contains(h2.stderr(), "crate:") {
		t.Errorf("the second Hold: expected a `crate:` line on stderr about podman's answer, got %q", h2.stderr())
	}
}
