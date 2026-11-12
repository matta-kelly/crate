package router_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/matta-kelly/crate/internal/machine"
	"github.com/matta-kelly/crate/internal/router"
)

// A reload that keeps failing for the limit is tried again, then Add takes the route file back and fails with
// Caddy's message.
func TestAReloadThatKeepsFailingTakesTheRouteBack(t *testing.T) {
	r := newRig(t)
	t.Setenv("CRATE_LIMIT_SCALE", "30")
	const message = "stand-in: the router did not take its routes"
	s := withPodmanStandIn(t, "reload", "always", message, 0)

	err := router.New(machine.Default()).Add(r.workspace, r.network, webAndAPI(r.workspace))
	if err == nil {
		t.Fatalf("Add: expected an error when the reload keeps failing")
	}
	if !strings.Contains(err.Error(), message) {
		t.Errorf("Add's error: expected Caddy's message %q, got %q", message, err)
	}
	if _, statErr := os.Stat(r.routeFile(r.workspace)); !os.IsNotExist(statErr) {
		t.Errorf("Add left the route file it wrote: %v", statErr)
	}
	if reloads := s.reloads(t); reloads < 2 {
		t.Errorf("a failing reload was tried %d time(s): expected it tried again", reloads)
	}
}

// A reload that keeps failing on Remove leaves the file gone and fails with Caddy's message.
func TestARemoveWithAFailingReloadLeavesTheFileGone(t *testing.T) {
	r := newRig(t)
	addRoutes(t, r.workspace, r.network, webAndAPI(r.workspace))
	const message = "stand-in: the router did not take its routes"
	withPodmanStandIn(t, "reload", "always", message, 0)

	err := router.New(machine.Default()).Remove(r.workspace, r.network)
	if err == nil {
		t.Fatalf("Remove: expected an error when the reload keeps failing")
	}
	if !strings.Contains(err.Error(), message) {
		t.Errorf("Remove's error: expected Caddy's message %q, got %q", message, err)
	}
	if _, statErr := os.Stat(r.routeFile(r.workspace)); !os.IsNotExist(statErr) {
		t.Errorf("Remove left the route file: %v", statErr)
	}
}

// A podman run that fails makes Add fail with podman's message.
func TestAPodmanRunThatFailsMakesAddFail(t *testing.T) {
	r := newRig(t)
	const message = "stand-in: podman run failed"
	withPodmanStandIn(t, "run", "once", message, 0)

	err := router.New(machine.Default()).Add(r.workspace, r.network, webAndAPI(r.workspace))
	if err == nil {
		t.Fatalf("Add: expected an error when podman run fails")
	}
	if !strings.Contains(err.Error(), message) {
		t.Errorf("Add's error: expected podman's message %q, got %q", message, err)
	}
}

// A podman start that fails makes Add fail with podman's message.
func TestAPodmanStartThatFailsMakesAddFail(t *testing.T) {
	r := newRig(t)
	startRouterByHand(t, r, "")
	podman(t, "stop", r.name)
	const message = "stand-in: podman start failed"
	withPodmanStandIn(t, "start", "once", message, 0)

	err := router.New(machine.Default()).Add(r.workspace, r.network, webAndAPI(r.workspace))
	if err == nil {
		t.Fatalf("Add: expected an error when podman start fails")
	}
	if !strings.Contains(err.Error(), message) {
		t.Errorf("Add's error: expected podman's message %q, got %q", message, err)
	}
}

// A podman network connect that fails makes Add fail with podman's message.
func TestANetworkConnectThatFailsMakesAddFail(t *testing.T) {
	r := newRig(t)
	addRoutes(t, r.workspace, r.network, webAndAPI(r.workspace))
	otherWorkspace, otherNetwork := r.peer("b")
	const message = "stand-in: podman network connect failed"
	withPodmanStandIn(t, "network connect", "once", message, 0)

	err := router.New(machine.Default()).Add(otherWorkspace, otherNetwork, webAndAPI(otherWorkspace))
	if err == nil {
		t.Fatalf("Add: expected an error when podman network connect fails")
	}
	if !strings.Contains(err.Error(), message) {
		t.Errorf("Add's error: expected podman's message %q, got %q", message, err)
	}
}

// A podman network disconnect that fails makes Remove fail with podman's message.
func TestANetworkDisconnectThatFailsMakesRemoveFail(t *testing.T) {
	r := newRig(t)
	addRoutes(t, r.workspace, r.network, webAndAPI(r.workspace))
	const message = "stand-in: podman network disconnect failed"
	withPodmanStandIn(t, "network disconnect", "once", message, 0)

	err := router.New(machine.Default()).Remove(r.workspace, r.network)
	if err == nil {
		t.Fatalf("Remove: expected an error when podman network disconnect fails")
	}
	if !strings.Contains(err.Error(), message) {
		t.Errorf("Remove's error: expected podman's message %q, got %q", message, err)
	}
}

// A remake that fails after the old router is gone leaves none, and the next Add makes it from the state folder.
func TestARemakeThatFailsLeavesNoRouterAndTheNextAddMakesIt(t *testing.T) {
	r := newRig(t)
	startTarget(t, r.name+"-target", r.network, r.workspace+"-app", map[int]string{3000: "workspace A"})

	// the copy's router: made by hand with a CPU limit, which crate remakes
	startRouterByHand(t, r, "1")

	const message = "stand-in: podman run failed"
	withPodmanStandIn(t, "run", "once", message, 0)
	err := router.New(machine.Default()).Add(r.workspace, r.network, webAndAPI(r.workspace))
	if err == nil {
		t.Fatalf("Add: expected an error when the remake's run fails")
	}
	if !strings.Contains(err.Error(), message) {
		t.Errorf("Add's error: expected podman's message %q, got %q", message, err)
	}
	if exec.Command(realPodman, "container", "exists", r.name).Run() == nil {
		t.Fatalf("a failed remake left a router")
	}

	if err := router.New(machine.Default()).Add(r.workspace, r.network, webAndAPI(r.workspace)); err != nil {
		t.Fatalf("the next Add: %v", err)
	}
	waitBody(t, 10*time.Second, r.port, r.workspace+".localhost", 200, "workspace A")
}

// Passing one of the router's limits is a failure that names it.
func TestAPassingRouterLimitIsNamed(t *testing.T) {
	t.Run("podman", func(t *testing.T) {
		r := newRig(t)
		t.Setenv("CRATE_LIMIT_SCALE", "30")
		withPodmanStandIn(t, "run", "once", "stand-in: too slow", 2*time.Second)
		err := router.New(machine.Default()).Add(r.workspace, r.network, webAndAPI(r.workspace))
		if err == nil {
			t.Fatalf("Add: expected an error when a podman call passes RouterPodman")
		}
		if !strings.Contains(err.Error(), "RouterPodman") {
			t.Errorf("Add's error does not name RouterPodman: %q", err)
		}
	})
	t.Run("reload", func(t *testing.T) {
		r := newRig(t)
		t.Setenv("CRATE_LIMIT_SCALE", "30")
		withPodmanStandIn(t, "reload", "always", "stand-in: too slow", 2*time.Second)
		err := router.New(machine.Default()).Add(r.workspace, r.network, webAndAPI(r.workspace))
		if err == nil {
			t.Fatalf("Add: expected an error when the reload passes RouterReload")
		}
		if !strings.Contains(err.Error(), "RouterReload") {
			t.Errorf("Add's error does not name RouterReload: %q", err)
		}
	})
}
