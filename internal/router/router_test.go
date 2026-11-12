package router_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matta-kelly/crate/internal/machine"
	"github.com/matta-kelly/crate/internal/machine/machinetest"
	"github.com/matta-kelly/crate/internal/router"
)

// The workspace's host and its first route's host reach the first route's port, another route reaches its own,
// and a host with no route is the 404.
func TestARouteAnswersAtTheWorkspaceAndAtItsName(t *testing.T) {
	r := newRig(t)
	startTarget(t, r.name+"-target", r.network, r.workspace+"-app", map[int]string{
		3000: "target on 3000",
		8080: "target on 8080",
	})
	addRoutes(t, r.workspace, r.network, webAndAPI(r.workspace))

	rows := []struct {
		host string
		want string
	}{
		{r.workspace + ".localhost", "target on 3000"},
		{"web." + r.workspace + ".localhost", "target on 3000"},
		{"web." + r.workspace + ".localhost:9999", "target on 3000"},
		{"api." + r.workspace + ".localhost", "target on 8080"},
		{"api." + r.workspace + ".localhost:" + strconv.Itoa(r.port), "target on 8080"},
	}
	for _, row := range rows {
		waitBody(t, 10*time.Second, r.port, row.host, 200, row.want)
	}
	waitBody(t, 10*time.Second, r.port, "other.localhost", 404, "no such workspace")
}

// The URL has the router's port when it is not 80, and has none when it is, the first route also at the
// workspace's own host, both read without the router serving at 80.
func TestTheURLFollowsTheRoutersPort(t *testing.T) {
	r := newRig(t)
	addRoutes(t, r.workspace, r.network, webAndAPI(r.workspace))

	got, err := r.urls(r.workspace)
	withPort := []string{
		"http://" + r.workspace + ".localhost:" + strconv.Itoa(r.port),
		"http://web." + r.workspace + ".localhost:" + strconv.Itoa(r.port),
		"http://api." + r.workspace + ".localhost:" + strconv.Itoa(r.port),
	}
	if err != nil || !reflect.DeepEqual(got, withPort) {
		t.Errorf("URLs at port %d: expected %v, got %v, %v", r.port, withPort, got, err)
	}

	t.Setenv("WORKSPACE_ROUTER_PORT", "")
	got, err = r.urls(r.workspace)
	atEighty := []string{
		"http://" + r.workspace + ".localhost",
		"http://web." + r.workspace + ".localhost",
		"http://api." + r.workspace + ".localhost",
	}
	if err != nil || !reflect.DeepEqual(got, atEighty) {
		t.Errorf("URLs at port 80: expected %v, got %v, %v", atEighty, got, err)
	}
}

// Adding the same list writes the same route file, reloads nothing and makes no router again.
func TestAddingTheSameRoutesChangesNothing(t *testing.T) {
	r := newRig(t)
	s := withPodmanStandIn(t, "none", "", "", 0)
	addRoutes(t, r.workspace, r.network, webAndAPI(r.workspace))

	before, err := os.ReadFile(r.routeFile(r.workspace))
	if err != nil {
		t.Fatalf("the route file after the first add: %v", err)
	}
	id := podman(t, "inspect", r.name, "--format", "{{.Id}}")
	reloads := s.reloads(t)
	reloaded := false
	for _, call := range s.calls(t) {
		if strings.Contains(call, "caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile") {
			reloaded = true
		}
	}
	if !reloaded {
		t.Errorf("the router was not reloaded by caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile")
	}

	addRoutes(t, r.workspace, r.network, webAndAPI(r.workspace))

	after, err := os.ReadFile(r.routeFile(r.workspace))
	if err != nil {
		t.Fatalf("the route file after the second add: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("the route file changed on the same list:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if again := podman(t, "inspect", r.name, "--format", "{{.Id}}"); again != id {
		t.Errorf("a second add made the router again: %s became %s", id, again)
	}
	if now := s.reloads(t); now != reloads {
		t.Errorf("a second add reloaded the router %d times", now-reloads)
	}
}

// A changed list replaces the old one, and only the new list answers.
func TestAddingAChangedListReplacesTheOld(t *testing.T) {
	r := newRig(t)
	startTarget(t, r.name+"-target", r.network, r.workspace+"-app", map[int]string{
		3000: "on 3000",
		8080: "on 8080",
		3001: "on 3001",
		8081: "on 8081",
	})
	addRoutes(t, r.workspace, r.network, webAndAPI(r.workspace))
	waitBody(t, 10*time.Second, r.port, "api."+r.workspace+".localhost", 200, "on 8080")

	addRoutes(t, r.workspace, r.network, []router.Route{
		{Name: "web", Target: r.workspace + "-app", Port: 3001},
		{Name: "docs", Target: r.workspace + "-app", Port: 8081},
	})

	waitBody(t, 10*time.Second, r.port, r.workspace+".localhost", 200, "on 3001")
	waitBody(t, 10*time.Second, r.port, "web."+r.workspace+".localhost", 200, "on 3001")
	waitBody(t, 10*time.Second, r.port, "docs."+r.workspace+".localhost", 200, "on 8081")
	waitBody(t, 10*time.Second, r.port, "api."+r.workspace+".localhost", 404, "no such workspace")
}

// The target sees the host the client asked for, with its port, and the request's scheme.
func TestTheTargetSeesTheForwardedHost(t *testing.T) {
	r := newRig(t)
	startTarget(t, r.name+"-target", r.network, r.workspace+"-app", map[int]string{
		3000: "{http.request.header.X-Forwarded-Host}|{http.request.header.X-Forwarded-Proto}",
	})
	addRoutes(t, r.workspace, r.network, []router.Route{
		{Name: "web", Target: r.workspace + "-app", Port: 3000},
	})

	rows := []string{
		"web." + r.workspace + ".localhost:" + strconv.Itoa(r.port),
		"web." + r.workspace + ".localhost",
	}
	for _, host := range rows {
		waitBody(t, 10*time.Second, r.port, host, 200, host+"|http")
	}
}

// Two workspaces on one router each answer from their own target, even with the same route name.
func TestTwoWorkspacesEachServeTheirOwnTarget(t *testing.T) {
	r := newRig(t)
	otherWorkspace, otherNetwork := r.peer("b")
	startTarget(t, r.name+"-a", r.network, r.workspace+"-app", map[int]string{3000: "workspace A"})
	startTarget(t, r.name+"-b", otherNetwork, otherWorkspace+"-app", map[int]string{3000: "workspace B"})

	addRoutes(t, r.workspace, r.network, []router.Route{
		{Name: "web", Target: r.workspace + "-app", Port: 3000},
	})
	addRoutes(t, otherWorkspace, otherNetwork, []router.Route{
		{Name: "web", Target: otherWorkspace + "-app", Port: 3000},
	})

	rows := []struct {
		host string
		want string
	}{
		{r.workspace + ".localhost", "workspace A"},
		{"web." + r.workspace + ".localhost", "workspace A"},
		{otherWorkspace + ".localhost", "workspace B"},
		{"web." + otherWorkspace + ".localhost", "workspace B"},
	}
	for _, row := range rows {
		waitBody(t, 10*time.Second, r.port, row.host, 200, row.want)
	}
}

// Removing a workspace's routes takes the file and the network away, its hosts answer 404, the other workspace
// still answers, the router stays, and removing it again changes nothing.
func TestRemovalTakesTheRouteFileAndTheNetworkOff(t *testing.T) {
	r := newRig(t)
	otherWorkspace, otherNetwork := r.peer("b")
	startTarget(t, r.name+"-a", r.network, r.workspace+"-app", map[int]string{3000: "workspace A"})
	startTarget(t, r.name+"-b", otherNetwork, otherWorkspace+"-app", map[int]string{3000: "workspace B"})
	addRoutes(t, r.workspace, r.network, []router.Route{
		{Name: "web", Target: r.workspace + "-app", Port: 3000},
	})
	addRoutes(t, otherWorkspace, otherNetwork, []router.Route{
		{Name: "web", Target: otherWorkspace + "-app", Port: 3000},
	})
	waitBody(t, 10*time.Second, r.port, r.workspace+".localhost", 200, "workspace A")

	remove := func() error {
		return router.New(machine.Default()).Remove(r.workspace, r.network)
	}
	if err := remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(r.routeFile(r.workspace)); !os.IsNotExist(err) {
		t.Errorf("Remove left the route file: %v", err)
	}
	waitBody(t, 10*time.Second, r.port, r.workspace+".localhost", 404, "no such workspace")
	waitBody(t, 10*time.Second, r.port, "web."+r.workspace+".localhost", 404, "no such workspace")
	waitBody(t, 10*time.Second, r.port, otherWorkspace+".localhost", 200, "workspace B")
	if exec.Command(realPodman, "container", "exists", r.name).Run() != nil {
		t.Errorf("Remove took the router itself away")
	}
	if networks := networksOf(t, r.name); strings.Contains(networks, r.network+" ") {
		t.Errorf("Remove left the router on %s: %s", r.network, networks)
	}
	if exec.Command(realPodman, "network", "exists", r.network).Run() != nil {
		t.Errorf("Remove took the workspace's network away, which is the caller's")
	}

	if err := remove(); err != nil {
		t.Errorf("Remove again: expected no error, got %v", err)
	}
	waitBody(t, 10*time.Second, r.port, otherWorkspace+".localhost", 200, "workspace B")
}

// A machine with no WORKSPACE_ROUTER_STATE takes its state folder from XDG_STATE_HOME/workspace-router, for
// the route files and the router's mounts.
func TestTheRouterDefaultsToTheXDGStateFolder(t *testing.T) {
	r := newRig(t)
	base := t.TempDir()
	t.Setenv("WORKSPACE_ROUTER_STATE", "")
	t.Setenv("XDG_STATE_HOME", base)
	addRoutes(t, r.workspace, r.network, webAndAPI(r.workspace))

	state := filepath.Join(base, "workspace-router")
	if _, err := os.Stat(filepath.Join(state, "routes", r.workspace+".caddy")); err != nil {
		t.Errorf("the route file under the default state folder: %v", err)
	}
	mounts := podman(t, "inspect", r.name, "--format", "{{range .Mounts}}{{.Destination}}={{.Source}} {{end}}")
	for _, destination := range []string{"/etc/caddy/Caddyfile", "/etc/caddy/routes", "/data", "/config"} {
		found := false
		for _, entry := range strings.Fields(mounts) {
			dst, source, ok := strings.Cut(entry, "=")
			if ok && dst == destination {
				found = true
				if !strings.HasPrefix(source, state+string(os.PathSeparator)) {
					t.Errorf("%s's source %s is not under %s", destination, source, state)
				}
			}
		}
		if !found {
			t.Errorf("the router's mounts lack %s: %s", destination, mounts)
		}
	}
}

// A machine with no WORKSPACE_ROUTER makes a router called workspace-router, without touching the real podman.
func TestTheRouterDefaultsToTheWorkspaceRouterName(t *testing.T) {
	r := newRig(t)
	t.Setenv("WORKSPACE_ROUTER", "")
	fake := withFakePodman(t)
	if err := router.New(machine.Default()).Add(r.workspace, r.network, webAndAPI(r.workspace)); err == nil {
		t.Fatalf("Add: expected the fake podman's run to fail")
	}
	found := false
	for _, call := range fake.calls(t) {
		if strings.HasPrefix(call, "run ") && strings.Contains(call, "--name workspace-router") {
			found = true
		}
	}
	if !found {
		t.Errorf("the router was not made with --name workspace-router: %q", fake.calls(t))
	}
}

// Removing a workspace's routes where the router was never made changes nothing.
func TestRemoveWithNoRouterChangesNothing(t *testing.T) {
	r := newRig(t)
	if err := router.New(machine.Default()).Remove(r.workspace, r.network); err != nil {
		t.Fatalf("Remove with no router and no route file: expected no error, got %v", err)
	}
	if exec.Command(realPodman, "network", "exists", r.network).Run() != nil {
		t.Errorf("Remove took the workspace's network away")
	}
}

// A route file with no route gives no URL and no error.
func TestURLsOfAWorkspaceWithNoRoutesIsEmpty(t *testing.T) {
	r := newRig(t)
	if err := os.MkdirAll(filepath.Join(r.state, "routes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.routeFile(r.workspace), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	urls, err := router.New(machine.Default()).URLs(r.workspace)
	if err != nil || urls != nil {
		t.Errorf("URLs of an empty route file: expected none and no error, got %v, %v", urls, err)
	}
}

// The router is one pinned Caddy container, made once on demand, placed through the machine in its slice, with
// the copy's mounts, its admin and a route file per workspace.
func TestTheRouterIsOneCaddyContainerPlacedInTheSlice(t *testing.T) {
	r := newRig(t)
	slice := testSlice(t)
	if exec.Command(realPodman, "container", "exists", r.name).Run() == nil {
		t.Fatalf("the router exists before an add")
	}
	recorder := &machinetest.Recorder{Slice: slice}
	rt := router.New(recorder)
	if err := rt.Add(r.workspace, r.network, webAndAPI(r.workspace)); err != nil {
		t.Fatalf("Add: %v", err)
	}

	var placed, held bool
	for _, call := range recorder.Calls {
		if call.Op == "Hold" {
			held = true
		}
		if call.Op == "Place" && call.Kind == "work" && call.System == "router" {
			placed = true
		}
	}
	if !placed {
		t.Errorf("the router was not placed through Place(\"work\", \"router\"): %+v", recorder.Calls)
	}
	if held {
		t.Errorf("the router held a slot: %+v", recorder.Calls)
	}

	if image := podman(t, "inspect", r.name, "--format", "{{.ImageName}}"); image != caddyImage {
		t.Errorf("the router's image: expected %s, got %s", caddyImage, image)
	}
	if cpus := podman(t, "inspect", r.name, "--format", "{{.HostConfig.NanoCpus}}"); cpus != "0" {
		t.Errorf("the router's NanoCpus: expected 0, got %s", cpus)
	}
	mounts := podman(t, "inspect", r.name, "--format", "{{range .Mounts}}{{.Source}}:{{.Destination}} {{end}}")
	for _, mount := range []string{
		r.state + "/Caddyfile:/etc/caddy/Caddyfile",
		r.state + "/routes:/etc/caddy/routes",
		r.state + "/data:/data",
		r.state + "/config:/config",
	} {
		if !strings.Contains(mounts, mount) {
			t.Errorf("the router's mounts lack %s: %s", mount, mounts)
		}
	}
	if port := podman(t, "port", r.name, "80/tcp"); port != "127.0.0.1:"+strconv.Itoa(r.port) {
		t.Errorf("the router's port: expected 127.0.0.1:%d, got %s", r.port, port)
	}
	caddyfile, err := os.ReadFile(r.state + "/Caddyfile")
	if err != nil {
		t.Fatalf("the Caddyfile: %v", err)
	}
	if !strings.Contains(string(caddyfile), "admin localhost:2019") {
		t.Errorf("the Caddyfile lacks the admin: %s", caddyfile)
	}
	if _, err := os.Stat(r.routeFile(r.workspace)); err != nil {
		t.Errorf("the route file: %v", err)
	}
	if cgroup := cgroupOf(t, r.name); !strings.Contains(cgroup, slice) {
		t.Errorf("the router's cgroup is not under %s: %s", slice, cgroup)
	}

	id := podman(t, "inspect", r.name, "--format", "{{.Id}}")
	if err := rt.Add(r.workspace, r.network, webAndAPI(r.workspace)); err != nil {
		t.Fatalf("the second Add: %v", err)
	}
	if again := podman(t, "inspect", r.name, "--format", "{{.Id}}"); again != id {
		t.Errorf("the router was made again: %s became %s", id, again)
	}
}

// The router's route files are one per workspace in the state folder, and its own log is in podman's.
func TestTheRouterKeepsItsRouteFilesAndItsLog(t *testing.T) {
	r := newRig(t)
	otherWorkspace, otherNetwork := r.peer("b")
	startTarget(t, r.name+"-a", r.network, r.workspace+"-app", map[int]string{3000: "workspace A"})
	addRoutes(t, r.workspace, r.network, []router.Route{
		{Name: "web", Target: r.workspace + "-app", Port: 3000},
	})
	addRoutes(t, otherWorkspace, otherNetwork, []router.Route{
		{Name: "web", Target: otherWorkspace + "-app", Port: 3000},
	})
	waitBody(t, 10*time.Second, r.port, r.workspace+".localhost", 200, "workspace A")

	for _, workspace := range []string{r.workspace, otherWorkspace} {
		if _, err := os.Stat(r.routeFile(workspace)); err != nil {
			t.Errorf("the route file of %s: %v", workspace, err)
		}
	}
	if logs := podman(t, "logs", r.name); strings.TrimSpace(logs) == "" {
		t.Errorf("podman logs of the router is empty")
	}
}
