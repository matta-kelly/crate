package router_test

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matta-kelly/crate/internal/machine"
	"github.com/matta-kelly/crate/internal/router"
)

const caddyImage = "docker.io/library/caddy:2.10"

var realPodman = func() string {
	p, err := exec.LookPath("podman")
	if err != nil {
		panic("router tests need podman on the PATH: " + err.Error())
	}
	return p
}()

var serial int64

func TestMain(m *testing.M) {
	if exec.Command(realPodman, "image", "exists", caddyImage).Run() != nil {
		if out, err := exec.Command(realPodman, "pull", caddyImage).CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "router tests need %s: %v\n%s", caddyImage, err, out)
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}

// podman runs a podman command the test's setup needs, and stops the test when it fails.
func podman(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command(realPodman, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("podman %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("a free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("a free port: %v", err)
	}
	return port
}

// rig is one test's router, its state, its network and its workspace.
type rig struct {
	t         *testing.T
	name      string
	port      int
	state     string
	network   string
	workspace string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	n := atomic.AddInt64(&serial, 1)
	suffix := fmt.Sprintf("%d-%d", os.Getpid(), n)
	r := &rig{
		t:         t,
		name:      "crate-router-" + suffix,
		port:      freePort(t),
		state:     t.TempDir(),
		network:   "crate-net-" + suffix,
		workspace: fmt.Sprintf("ws%d", n),
	}
	t.Setenv("WORKSPACE_ROUTER", r.name)
	t.Setenv("WORKSPACE_ROUTER_PORT", strconv.Itoa(r.port))
	t.Setenv("WORKSPACE_ROUTER_STATE", r.state)
	podman(t, "network", "create", r.network)
	t.Cleanup(func() {
		exec.Command(realPodman, "rm", "-f", "-t", "0", r.name).Run()
		exec.Command(realPodman, "network", "rm", "-f", r.network).Run()
		exec.Command(realPodman, "network", "rm", "-f", r.name).Run()
	})
	return r
}

// peer makes a second workspace on its own network, under the same router.
func (r *rig) peer(label string) (workspace, network string) {
	workspace = "ws-" + label
	network = r.name + "-" + label
	podman(r.t, "network", "create", network)
	r.t.Cleanup(func() { exec.Command(realPodman, "network", "rm", "-f", network).Run() })
	return workspace, network
}

func (r *rig) routeFile(workspace string) string {
	return filepath.Join(r.state, "routes", workspace+".caddy")
}

func (r *rig) urls(workspace string) ([]string, error) {
	return router.New(machine.Default()).URLs(workspace)
}

// startTarget puts a container on network, under alias, that answers each port with its body.
func startTarget(t *testing.T, name, network, alias string, sites map[int]string) {
	t.Helper()
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("{\n\tadmin off\n\tauto_https off\n}\n")
	for _, port := range sortedPorts(sites) {
		fmt.Fprintf(&b, ":%d {\n\trespond %q\n}\n", port, sites[port])
	}
	if err := os.WriteFile(filepath.Join(dir, "Caddyfile"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	podman(t, "run", "-d", "--name", name, "--network", network, "--network-alias", alias,
		"-v", filepath.Join(dir, "Caddyfile")+":/etc/caddy/Caddyfile:Z", caddyImage)
	t.Cleanup(func() { exec.Command(realPodman, "rm", "-f", "-t", "0", name).Run() })
}

func sortedPorts(sites map[int]string) []int {
	ports := make([]int, 0, len(sites))
	for port := range sites {
		ports = append(ports, port)
	}
	for i := 1; i < len(ports); i++ {
		for j := i; j > 0 && ports[j] < ports[j-1]; j-- {
			ports[j], ports[j-1] = ports[j-1], ports[j]
		}
	}
	return ports
}

func tryGet(port int, host string) (int, string, error) {
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", port), nil)
	if err != nil {
		return 0, "", err
	}
	req.Host = host
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{}}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), err
}

// waitBody waits until host answers with status and body, and fails with the last answer when it does not.
func waitBody(t *testing.T, within time.Duration, port int, host string, status int, body string) {
	t.Helper()
	deadline := time.Now().Add(within)
	var gotStatus int
	var gotBody string
	var err error
	for time.Now().Before(deadline) {
		gotStatus, gotBody, err = tryGet(port, host)
		if err == nil && gotStatus == status && gotBody == body {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("GET with Host %q on 127.0.0.1:%d: expected %d %q, got %d %q (last error %v)",
		host, port, status, body, gotStatus, gotBody, err)
}

func webAndAPI(workspace string) []router.Route {
	return []router.Route{
		{Name: "web", Target: workspace + "-app", Port: 3000},
		{Name: "api", Target: workspace + "-app", Port: 8080},
	}
}

// standIn is a podman on the PATH that logs each call and can fail a call once, or always.
type standIn struct {
	log string
}

func (s *standIn) calls(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(s.log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func (s *standIn) reloads(t *testing.T) int {
	t.Helper()
	n := 0
	for _, call := range s.calls(t) {
		if strings.Contains(call, "caddy reload") {
			n++
		}
	}
	return n
}

// withPodmanStandIn puts a podman on the PATH that logs each call to s.log. When fail is one of run, start,
// "network connect", "network disconnect" or reload, the first such call (or every one, in always mode) waits
// sleep, writes message to stderr and fails.
func withPodmanStandIn(t *testing.T, fail, mode, message string, sleep time.Duration) *standIn {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	mark := filepath.Join(dir, "failed-once")
	const rawScript = `#!/usr/bin/env bash
printf '%s\n' "$*" >> "{{LOG}}"
hit=0
case "{{FAIL}}" in
  run) [ "${1:-}" = run ] && hit=1 ;;
  start) [ "${1:-}" = start ] && hit=1 ;;
  "network connect") [ "${1:-}" = network ] && [ "${2:-}" = connect ] && hit=1 ;;
  "network disconnect") [ "${1:-}" = network ] && [ "${2:-}" = disconnect ] && hit=1 ;;
  reload) [ "${1:-}" = exec ] && case "$*" in *"caddy reload"*) hit=1 ;; esac ;;
esac
if [ "$hit" = 1 ]; then
  if [ "{{MODE}}" = always ] || [ ! -e "{{MARK}}" ]; then
    touch "{{MARK}}"
    sleep {{SLEEP}}
    printf '%s\n' "{{MESSAGE}}" >&2
    exit 1
  fi
fi
exec "{{REAL}}" "$@"
`
	script := strings.NewReplacer(
		"{{LOG}}", log,
		"{{MARK}}", mark,
		"{{FAIL}}", fail,
		"{{MODE}}", mode,
		"{{SLEEP}}", strconv.FormatFloat(sleep.Seconds(), 'f', -1, 64),
		"{{MESSAGE}}", message,
		"{{REAL}}", realPodman,
	).Replace(rawScript)
	path := filepath.Join(dir, "podman")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &standIn{log: log}
}

// withFakePodman puts a podman on the PATH that never runs the real one: it logs each call, says the router's
// network exists and its container does not, and lets a make succeed. When hang is run or reload, that call
// sleeps until its limit passes; every other call fails. So no person's router is touched.
func withFakePodman(t *testing.T, hang string, sleep time.Duration) *standIn {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	const raw = `#!/usr/bin/env bash
printf '%s\n' "$*" >> "{{LOG}}"
hit=0
case "{{HANG}}" in
  run) [ "${1:-}" = run ] && hit=1 ;;
  reload) [ "${1:-}" = exec ] && case "$*" in *"caddy reload"*) hit=1 ;; esac ;;
esac
if [ "$hit" = 1 ]; then
  exec sleep {{SLEEP}}
fi
case "${1:-}" in
  network) [ "${2:-}" = exists ] && exit 0; exit 1 ;;
  container) [ "${2:-}" = exists ] && exit 1; exit 1 ;;
  run) exit 0 ;;
esac
exit 1
`
	script := strings.NewReplacer(
		"{{LOG}}", log,
		"{{HANG}}", hang,
		"{{SLEEP}}", strconv.Itoa(int(sleep/time.Second)),
	).Replace(raw)
	path := filepath.Join(dir, "podman")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &standIn{log: log}
}

// testSlice makes a slice of the test's own, and removes it when the test ends.
func testSlice(t *testing.T) string {
	t.Helper()
	n := atomic.AddInt64(&serial, 1)
	slice := fmt.Sprintf("crate-router-test-%d-%d.slice", os.Getpid(), n)
	unit := strings.TrimSuffix(slice, ".slice") + "-keep"
	out, err := exec.Command("systemd-run", "--user", "--unit", unit, "--slice", slice, "sleep", "120").CombinedOutput()
	if err != nil {
		t.Fatalf("systemd-run %s: %v\n%s", slice, err, out)
	}
	t.Cleanup(func() { exec.Command("systemctl", "--user", "stop", unit).Run() })
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out, err := exec.Command("systemctl", "--user", "show", slice, "--property=ActiveState", "--value").Output()
		if err == nil && strings.TrimSpace(string(out)) == "active" {
			return slice
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the slice %s did not become active", slice)
	return ""
}

func networksOf(t *testing.T, name string) string {
	t.Helper()
	return podman(t, "inspect", name, "--format", "{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}")
}

func hasNetwork(networks, network string) bool {
	for _, name := range strings.Fields(networks) {
		if name == network {
			return true
		}
	}
	return false
}

// startRouterByHand makes a router of the state folder and mounts, as the copy's workspace.sh does, with a CPU
// limit when cpu is not empty. It is setup for the failures a make or a start can meet.
func startRouterByHand(t *testing.T, r *rig, cpu string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(r.state, "routes"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"data", "config"} {
		if err := os.MkdirAll(filepath.Join(r.state, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	const caddyfile = `{
	admin localhost:2019
	auto_https off
}
:80 {
	import /etc/caddy/routes/*.caddy
	handle {
		respond "no such workspace" 404
	}
}
`
	if err := os.WriteFile(filepath.Join(r.state, "Caddyfile"), []byte(caddyfile), 0o644); err != nil {
		t.Fatal(err)
	}
	if exec.Command(realPodman, "network", "exists", r.name).Run() != nil {
		podman(t, "network", "create", r.name)
	}
	args := []string{"run", "-d", "--name", r.name, "--network", r.name}
	if cpu != "" {
		args = append(args, "--cpus", cpu, "--cpu-shares", "256")
	}
	args = append(args,
		"-p", "127.0.0.1:"+strconv.Itoa(r.port)+":80",
		"-v", filepath.Join(r.state, "Caddyfile")+":/etc/caddy/Caddyfile:Z",
		"-v", filepath.Join(r.state, "routes")+":/etc/caddy/routes:Z",
		"-v", filepath.Join(r.state, "data")+":/data:Z",
		"-v", filepath.Join(r.state, "config")+":/config:Z",
		caddyImage)
	podman(t, args...)
}

func cgroupOf(t *testing.T, name string) string {
	t.Helper()
	pid := podman(t, "inspect", name, "--format", "{{.State.Pid}}")
	data, err := os.ReadFile(filepath.Join("/proc", pid, "cgroup"))
	if err != nil {
		t.Fatalf("read the cgroup of pid %s: %v", pid, err)
	}
	return string(data)
}

func writeDevcontainer(t *testing.T, root, repo string, port int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".devcontainer"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("{\n  \"name\": %q,\n  \"forwardPorts\": [%d],\n  \"image\": \"unused\"\n}\n", repo, port)
	if err := os.WriteFile(filepath.Join(root, ".devcontainer", "devcontainer.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// runWorkspace runs the copy's workspace.sh with this test's router environment.
func runWorkspace(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{"testdata/workspace.sh"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("workspace.sh %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func addRoutes(t *testing.T, workspace, network string, routes []router.Route) {
	t.Helper()
	if err := router.New(machine.Default()).Add(workspace, network, routes); err != nil {
		t.Fatalf("Add(%q, %q): %v", workspace, network, err)
	}
}
