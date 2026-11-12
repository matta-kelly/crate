package machine_test

// The calls: every systemctl, systemd-run and podman call goes through one helper with a limit, and a
// failure returns one error that names the call, the unit and the tool's message.

import (
	"strings"
	"testing"
	"time"

	"github.com/matta-kelly/crate/internal/machine"
)

// A tool that fails gives one error naming the call, a unit of the run and the tool's message.
func TestAFailedCallGivesOneErrorNamingTheCallTheUnitAndTheMessage(t *testing.T) {
	f := newFixture(t, 1, 1, 1)
	system := f.system("a")
	fails := `#!/bin/sh
for a in "$@"; do last=$a; done
echo "Failed to start unit $last: Access denied" >&2
exit 1
`
	path := f.pathWith(map[string]string{"systemctl": fails, "systemd-run": fails})
	h := f.startHelper("place", machine.Work, system, path...)
	out := h.waitOut(t, 30*time.Second)
	message, ok := strings.CutPrefix(out, "err ")
	if !ok {
		t.Fatalf("Place with a tool that fails: expected an error, got %q", out)
	}
	if !strings.Contains(message, "systemctl") && !strings.Contains(message, "systemd-run") {
		t.Errorf("the error: expected it to name the systemd call, got %q", message)
	}
	for _, want := range []string{f.slice, "Access denied"} {
		if !strings.Contains(message, want) {
			t.Errorf("the error: expected it to name %q, got %q", want, message)
		}
	}
}

// A tool that hangs stops at the call's limit, and the error names the call.
func TestACallThatHangsStopsAtItsLimit(t *testing.T) {
	f := newFixture(t, 1, 1, 1)
	hangs := "#!/bin/sh\nsleep 60\n"
	path := f.pathWith(map[string]string{"systemctl": hangs, "systemd-run": hangs})
	started := time.Now()
	h := f.startHelper("place", machine.Work, f.system("a"), append(path, scaleFast)...)
	out := h.waitOut(t, 30*time.Second)
	took := time.Since(started)
	if _, ok := strings.CutPrefix(out, "err "); !ok {
		t.Fatalf("Place with a tool that hangs: expected an error past limit.Systemd, got %q", out)
	}
	if !strings.Contains(out, "systemctl") && !strings.Contains(out, "systemd-run") {
		t.Errorf("the error: expected it to name the systemd call, got %q", out)
	}
	if took < 500*time.Millisecond || took > 10*time.Second {
		t.Errorf("the call past its limit: expected about one second (30 s divided by 3600), took %v", took)
	}
}

// A podman call that cannot answer stops at its limit and is named on stderr, so the slot stays held.
func TestAPodmanCallThatCannotAnswerStopsAtItsLimitAndIsNamed(t *testing.T) {
	f := newFixture(t, 1, 1, 1)
	first := f.system("a")
	releaseFirst := f.absent()
	h1 := f.startHelper("hold-bind", machine.Attempt, first,
		"HELPER_NETWORK="+first, "HELPER_RELEASE="+releaseFirst)
	f.networks = append(f.networks, first)
	requireHeld(t, h1.waitOut(t, 30*time.Second), "the first holder")

	hangs := `#!/bin/sh
case "$1 $2" in
"network exists") sleep 60 ;;
esac
exit 0
`
	path := f.pathWith(map[string]string{"podman": hangs})
	started := time.Now()
	h2 := f.startHelper("hold", machine.Attempt, f.system("b"),
		append(path, scaleFast, "HELPER_RELEASE="+f.absent())...)
	out := h2.waitOut(t, 30*time.Second)
	took := time.Since(started)
	if _, ok := strings.CutPrefix(out, "err "); !ok {
		t.Fatalf("a Hold whose podman call hangs: expected the wait to stop, got %q", out)
	}
	if !strings.Contains(h2.stderr(), "podman network exists") {
		t.Errorf("stderr: expected it to name the podman call, got %q", h2.stderr())
	}
	if took > 10*time.Second {
		t.Errorf("the podman call past its limit: expected it to stop within the Hold's wait, took %v", took)
	}
	writeFile(t, releaseFirst)
	h1.waitExit(t, 30*time.Second)
	h2.waitExit(t, 30*time.Second)
}
