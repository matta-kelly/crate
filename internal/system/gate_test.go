package system_test

// The gate's case through the API, the seam to the machine, and the calls that fail.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matta-kelly/crate/internal/system"
)

// The gate's case: a check named as the gate names it, an extra read-only mount at its host path, an extra
// env value, and the volumes to keep. Remove keeps the named volume with what the setup wrote, and leaves no
// container, pod, network or service volume. The same for postsubmit.
func TestTheGatesCaseThroughTheAPI(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{system.Check, system.Postsubmit} {
		t.Run(kind, func(t *testing.T) {
			mount := t.TempDir()
			if err := os.WriteFile(filepath.Join(mount, "note.txt"), []byte("from-mount\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			name := fmt.Sprintf("crate-test-%d-%d-%s", os.Getpid(), serial.Add(1), kind)
			keep := name + "-node-modules"
			w := newWorld(t, worldOpts{
				Kind:     kind,
				Name:     name,
				Setup:    "mkdir -p node_modules && echo written > node_modules/out",
				Services: true,
				Mounts:   []system.Mount{{Path: mount}},
				Env:      []string{"GATE_ENV=seen"},
				Keep:     []string{keep},
			})
			w.up()

			if got := w.mustRun("cat", filepath.Join(mount, "note.txt")); strings.TrimSpace(got) != "from-mount" {
				t.Errorf("the extra mount: expected its file at the host's path, got %q", got)
			}
			if r := w.run("sh", "-c", "echo x > "+filepath.Join(mount, "other.txt")); r.err == nil && r.code == 0 {
				t.Errorf("the extra mount: expected it read-only, the write worked: %q", r.out)
			}
			if got := w.mustRun("printenv", "GATE_ENV"); strings.TrimSpace(got) != "seen" {
				t.Errorf("the extra env: expected seen, got %q", got)
			}

			if err := w.sys.Remove([]string{keep}); err != nil {
				t.Fatalf("Remove: %v", err)
			}
			if got := w.podmanOK("run", "--rm", "-v", keep+":/v", alpine, "cat", "/v/out"); strings.TrimSpace(got) != "written" {
				t.Errorf("the kept volume: expected what setup wrote, got %q", got)
			}
			for _, left := range w.leftovers() {
				if left == keep {
					continue
				}
				t.Errorf("after Remove: expected only %q, found %q", keep, left)
			}
		})
	}
}

// Up with a ctx that ends during setup says it ran past its limit and leaves nothing but the kept volumes.
func TestUpPastItsLimitLeavesOnlyTheKeptVolumes(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOpts{
		Kind:     system.Work,
		Services: true,
		Setup:    "sleep 60",
		Keep:     nil,
	})
	if err := w.sys.Hold(); err != nil {
		t.Fatalf("Hold: %v", err)
	}
	if err := w.sys.Image(); err != nil {
		t.Fatalf("Image: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := w.sys.Up(ctx)
	if err == nil {
		t.Fatal("Up past its ctx: expected an error, got none")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(strings.ToLower(err.Error()), "limit") {
		t.Errorf("Up past its ctx: expected it to say it ran past its limit, got %v", err)
	}
	for _, left := range w.leftovers() {
		t.Errorf("after Up past its limit: expected nothing but the kept volumes, found %q", left)
	}
}

// The seam: one Hold before the first Place, one Bind after the network and before the first service or
// member, a Place for each member, each service and the build, and a Release only on Remove.
func TestTheSeamHoldsBindsAndPlaces(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOpts{Kind: system.Check, Services: true, Setup: "true"})
	w.up()

	calls := w.rec.Calls
	if len(calls) == 0 || calls[0].Op != "Hold" {
		t.Fatalf("the calls: expected a Hold first, got %+v", calls)
	}
	counts := map[string]int{}
	firstPlace, bind := -1, -1
	for i, c := range calls {
		counts[c.Op]++
		if c.Op == "Place" && firstPlace < 0 {
			firstPlace = i
		}
		if c.Op == "Bind" {
			bind = i
		}
		if c.Kind != system.Check || c.System != w.name {
			t.Errorf("call %d: expected kind %q and system %q, got %+v", i+1, system.Check, w.name, c)
		}
	}
	if counts["Hold"] != 1 || counts["Bind"] != 1 || counts["Release"] != 0 {
		t.Errorf("the calls: expected one Hold, one Bind and no Release after up, got %+v", counts)
	}
	if firstPlace < 0 {
		t.Errorf("the calls: expected a Place after the Hold, got %+v", calls)
	}
	if counts["Place"] < 3 {
		t.Errorf("the calls: expected a Place for the build, each service and the member, got %+v", calls)
	}
	placesAfterBind := 0
	for _, c := range calls[bind+1:] {
		if c.Op == "Place" {
			placesAfterBind++
		}
	}
	if placesAfterBind < 2 {
		t.Errorf("the calls: expected the service and the member placed after the Bind, got %+v", calls)
	}

	// A placement of none: the member's container has no cgroup parent.
	if got := w.podmanOK("inspect", w.name+"-tools", "--format", "{{.HostConfig.CgroupParent}}"); got != "" {
		t.Errorf("the tools member: expected no cgroup parent, got %q", got)
	}

	before := len(w.rec.Calls)
	if err := w.sys.Remove(nil); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	releases := 0
	for _, c := range w.rec.Calls[before:] {
		if c.Op == "Release" {
			releases++
		}
	}
	if releases != 1 {
		t.Errorf("Remove: expected one Release, got %d: %+v", releases, w.rec.Calls[before:])
	}
}

// --- the failed ask ---

// podmanWrapper puts a podman first on PATH that fails one call with a message, and forwards every other
// call to the real podman. With once, only the first matching call fails.
func podmanWrapper(t *testing.T, fail string, once bool) {
	t.Helper()
	real, err := exec.LookPath("podman")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	state := filepath.Join(dir, "failed")
	guard := ""
	if once {
		guard = fmt.Sprintf("[ -e %q ] || { : >%q; ", state, state)
		guard += fmt.Sprintf("echo %q >&2; exit 125; }", "podman "+fail+" exploded")
	} else {
		guard = fmt.Sprintf("echo %q >&2; exit 125", "podman "+fail+" exploded")
	}
	body := fmt.Sprintf("#!/bin/sh\ncase \"$1 $2\" in\n%s)\n  %s\n  ;;\nesac\nexec %q \"$@\"\n", fmt.Sprintf("%q*", fail), guard, real)
	if err := os.WriteFile(filepath.Join(dir, "podman"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A podman call that fails in Up makes Up fail with the step and podman's message, and leaves nothing of the
// system but the kept volumes.
func TestAFailedAskFailsUpAndLeavesNothing(t *testing.T) {
	for _, row := range []struct{ name, call, want string }{
		{"the network", "network create", "network create"},
		{"a kube play", "kube play", "kube play"},
		{"a run", "run", "run"},
		{"a volume", "volume create", "volume create"},
	} {
		t.Run(row.name, func(t *testing.T) {
			w := newWorld(t, worldOpts{Kind: system.Work, Services: true, Setup: "true"})
			podmanWrapper(t, row.call, false)
			if err := w.sys.Hold(); err != nil {
				t.Fatalf("Hold: %v", err)
			}
			if err := w.sys.Image(); err != nil {
				t.Fatalf("Image: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			err := w.sys.Up(ctx)
			if err == nil || !strings.Contains(err.Error(), "exploded") || !strings.Contains(err.Error(), row.want) {
				t.Fatalf("Up with a failed %s: expected the step and podman's message %q, got %v", row.name, "podman "+row.call+" exploded", err)
			}
			if left := w.leftovers(); len(left) > 0 {
				t.Errorf("after a failed %s: expected nothing, found %q", row.name, left)
			}
		})
	}
}

// A second Up of the same name after a kill leaves no member of the first, and keeps the volume the caller
// names with what the first left in it.
func TestASecondUpAfterAKillLeavesNoMemberOfTheFirst(t *testing.T) {
	t.Parallel()
	name := fmt.Sprintf("cratetest-%d-%d", os.Getpid(), serial.Add(1))
	keep := name + "-node-modules"
	w := newWorld(t, worldOpts{Kind: system.Check, Name: name, Services: true, Setup: "true", Keep: []string{keep}})
	w.up()
	w.mustRun("sh", "-c", "echo kept > node_modules/keepmark")
	old := w.podmanOK("inspect", w.name+"-tools", "--format", "{{.Id}}")
	w.podmanOK("kill", w.name+"-tools")

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := w.sys.Up(ctx); err != nil {
		t.Fatalf("Up after a kill: expected the system again, got %v", err)
	}
	if got := w.mustRun("cat", "node_modules/keepmark"); strings.TrimSpace(got) != "kept" {
		t.Errorf("the kept volume: expected the mark the first left, got %q", got)
	}
	w.podmanOK("volume", "inspect", keep)
	if got := w.podmanOK("inspect", w.name+"-tools", "--format", "{{.Id}}"); got == old {
		t.Errorf("the tools member: expected a new one, still %s", old)
	}
	if got := w.podmanOK("inspect", w.name+"-tools", "--format", "{{.State.Running}}"); got != "true" {
		t.Errorf("the tools member: expected it running, got %q", got)
	}
	if _, err := w.podman("container", "exists", old); err == nil {
		t.Errorf("the first member %s: expected it gone", old)
	}
}

// A failed Image returns the call's error, leaves the slot held, and Remove makes the one Release.
func TestAFailedImageReturnsItsCallErrorAndKeepsTheSlotUntilRemove(t *testing.T) {
	w := newWorld(t, worldOpts{Kind: system.Check, NoDevImage: true, Setup: "true"})
	podmanWrapper(t, "build", false)
	if err := w.sys.Hold(); err != nil {
		t.Fatalf("Hold: %v", err)
	}
	places := 0
	err := w.sys.Image()
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "build") {
		t.Fatalf("Image with a failed build: expected the call's error, got %v", err)
	}
	for _, c := range w.rec.Calls {
		if c.Op == "Place" {
			places++
		}
	}
	if !strings.Contains(w.out.String(), "exploded") {
		t.Errorf("Image's output: expected the call's own message, got %q", w.out.String())
	}
	if places == 0 {
		t.Errorf("Image: expected a Place before the build, got %+v", w.rec.Calls)
	}
	before := len(w.rec.Calls)
	if err := w.sys.Remove(nil); err != nil {
		t.Fatalf("Remove after a failed Image: %v", err)
	}
	releases := 0
	for _, c := range w.rec.Calls[before:] {
		if c.Op == "Release" {
			releases++
		}
	}
	if releases != 1 {
		t.Errorf("Remove after a failed Image: expected one Release, got %d: %+v", releases, w.rec.Calls[before:])
	}
}

// A down whose podman call fails exits with an error that names what is left; a second down finishes it.
func TestADownThatFailsNamesWhatIsLeftAndASecondFinishes(t *testing.T) {
	w := newWorld(t, worldOpts{Kind: system.Work, Services: true})
	w.up()
	podmanWrapper(t, "network rm", true)

	err := w.sys.Remove(nil)
	if err == nil || !strings.Contains(err.Error(), w.name) {
		t.Fatalf("a down whose network rm fails: expected an error that names what is left (%q), got %v", w.name, err)
	}
	if err := w.sys.Remove(nil); err != nil {
		t.Fatalf("a second down: expected it to finish, got %v", err)
	}
	if left := w.leftovers(); len(left) > 0 {
		t.Errorf("after the second down: expected nothing, got %q", left)
	}
}

