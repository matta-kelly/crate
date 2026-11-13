package router_test

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matta-kelly/crate/internal/machine/machinetest"
	"github.com/matta-kelly/crate/internal/router"
)

// The router made by the copy's workspace.sh is made again once by crate, in its slice and with no CPU limit,
// on every network it was on; both workspaces answer; then the copy's up and down of a second folder go through
// crate's router and crate's routes still answer; and crate's removal leaves the copy's first route.
func TestTheCopysRouterIsRemadeOnceAndKeepsBothWorkspaces(t *testing.T) {
	r := newRig(t)
	t.Cleanup(func() {
		exec.Command(realPodman, "network", "rm", "-f", "wstest-one", "wstest-two").Run()
	})
	slice := testSlice(t)

	rootOne := filepath.Join(t.TempDir(), "one")
	writeDevcontainer(t, rootOne, "wstest", 3000)
	runWorkspace(t, "up", rootOne)
	const copyOne, copyTwo = "wstest-one", "wstest-two"

	old := podman(t, "inspect", r.name, "--format", "{{.Id}}")
	if cpus := podman(t, "inspect", r.name, "--format", "{{.HostConfig.NanoCpus}}"); cpus == "0" {
		t.Fatalf("the copy's router has no CPU limit: NanoCpus %s", cpus)
	}
	if networks := networksOf(t, r.name); !hasNetwork(networks, copyOne) {
		t.Fatalf("the copy's router is not on %s: %s", copyOne, networks)
	}

	startTarget(t, r.name+"-copy-one", copyOne, copyOne+"-app", map[int]string{3000: "copy one"})
	t.Cleanup(func() { exec.Command("bash", "testdata/workspace.sh", "down", rootOne).Run() })

	const crateWorkspace = "crate-one"
	startTarget(t, r.name+"-crate", r.network, crateWorkspace+"-app", map[int]string{3000: "crate one"})
	rt := router.New(&machinetest.Recorder{Slice: slice})
	if err := rt.Add(crateWorkspace, r.network, []router.Route{
		{Name: "web", Target: crateWorkspace + "-app", Port: 3000},
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if made := podman(t, "inspect", r.name, "--format", "{{.Id}}"); made == old {
		t.Errorf("Add did not make the copy's router again")
	}
	if cpus := podman(t, "inspect", r.name, "--format", "{{.HostConfig.NanoCpus}}"); cpus != "0" {
		t.Errorf("the remade router's NanoCpus: expected 0, got %s", cpus)
	}
	networks := networksOf(t, r.name)
	for _, network := range []string{copyOne, r.name, r.network} {
		if !hasNetwork(networks, network) {
			t.Errorf("the remade router is not on %s: %s", network, networks)
		}
	}
	if port := podman(t, "port", r.name, "80/tcp"); port != "127.0.0.1:"+strconv.Itoa(r.port) {
		t.Errorf("the remade router's port: expected 127.0.0.1:%d, got %s", r.port, port)
	}
	if cgroup := cgroupOf(t, r.name); !strings.Contains(cgroup, slice) {
		t.Errorf("the remade router's cgroup is not under %s: %s", slice, cgroup)
	}
	waitBody(t, 5*time.Second, r.port, copyOne+".localhost", 200, "copy one")
	waitBody(t, 5*time.Second, r.port, crateWorkspace+".localhost", 200, "crate one")
	waitBody(t, 5*time.Second, r.port, "web."+crateWorkspace+".localhost", 200, "crate one")

	remade := podman(t, "inspect", r.name, "--format", "{{.Id}}")
	if err := rt.Add(crateWorkspace, r.network, []router.Route{
		{Name: "web", Target: crateWorkspace + "-app", Port: 3000},
	}); err != nil {
		t.Fatalf("the second Add: %v", err)
	}
	if again := podman(t, "inspect", r.name, "--format", "{{.Id}}"); again != remade {
		t.Errorf("a second add made the router again: %s became %s", remade, again)
	}

	rootTwo := filepath.Join(t.TempDir(), "two")
	writeDevcontainer(t, rootTwo, "wstest", 3001)
	runWorkspace(t, "up", rootTwo)
	startTarget(t, r.name+"-copy-two", copyTwo, copyTwo+"-app", map[int]string{3001: "copy two"})
	waitBody(t, 10*time.Second, r.port, copyTwo+".localhost", 200, "copy two")
	waitBody(t, 10*time.Second, r.port, crateWorkspace+".localhost", 200, "crate one")
	waitBody(t, 10*time.Second, r.port, copyOne+".localhost", 200, "copy one")

	// the copy's down needs its network free
	exec.Command(realPodman, "rm", "-f", "-t", "0", r.name+"-copy-two").Run()
	runWorkspace(t, "down", rootTwo)
	waitBody(t, 10*time.Second, r.port, copyTwo+".localhost", 404, "no such workspace")
	waitBody(t, 10*time.Second, r.port, crateWorkspace+".localhost", 200, "crate one")
	waitBody(t, 10*time.Second, r.port, copyOne+".localhost", 200, "copy one")

	if err := rt.Remove(crateWorkspace, r.network); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	waitBody(t, 10*time.Second, r.port, crateWorkspace+".localhost", 404, "no such workspace")
	waitBody(t, 10*time.Second, r.port, copyOne+".localhost", 200, "copy one")
	if exec.Command(realPodman, "container", "exists", r.name).Run() != nil {
		t.Errorf("Remove took the copy's router away")
	}
}

// A router made on another host port, with no CPU limit, is made again on the router's port, and serves there.
func TestARouterOnAnotherPortIsRemadeOnTheRoutersPort(t *testing.T) {
	r := newRig(t)
	startRouterByHand(t, r, "")
	old := podman(t, "inspect", r.name, "--format", "{{.Id}}")
	r.port = freePort(t)
	t.Setenv("WORKSPACE_ROUTER_PORT", strconv.Itoa(r.port))

	startTarget(t, r.name+"-target", r.network, r.workspace+"-app", map[int]string{3000: "on the new port"})
	addRoutes(t, r.workspace, r.network, []router.Route{{Name: "web", Target: r.workspace + "-app", Port: 3000}})

	if made := podman(t, "inspect", r.name, "--format", "{{.Id}}"); made == old {
		t.Errorf("Add did not make the router on the old port again")
	}
	if port := podman(t, "port", r.name, "80/tcp"); port != "127.0.0.1:"+strconv.Itoa(r.port) {
		t.Errorf("the remade router's port: expected 127.0.0.1:%d, got %s", r.port, port)
	}
	waitBody(t, 10*time.Second, r.port, r.workspace+".localhost", 200, "on the new port")
}
