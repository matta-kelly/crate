package machine_test

// The fixture of the machine tests: a [count] file, a slice name and a state folder of the test's own, on
// real podman and the user's systemd. A test acts on the machine through a helper process of its own, under
// the environment the fixture names, so the tests run in parallel. Each test removes its units, containers
// and networks.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matta-kelly/crate/internal/machine"
)

// testImage is the pinned alpine the shell tests also use: a container that needs no network to start.
const testImage = "docker.io/library/alpine@sha256:0f49f331d9212aff11b932863be4eb01cdf962969aa86dab7d1ee21ac6f5f52b"

// helperMarker is set only in a process the test starts to act on the machine, so TestHelperProcess returns
// at once when the test runner calls it as an ordinary test.
const helperMarker = "CRATE_MACHINE_TEST_HELPER"

// scaleFast divides every limit to a second, so a call that should not wait fails within the test's reach.
const scaleFast = "CRATE_LIMIT_SCALE=3600"

type fixture struct {
	t          *testing.T
	slice      string
	tag        string
	state      string
	machine    string
	env        []string
	containers []string
	networks   []string
}

// newFixture writes a [count] file and names a slice, a state folder and a tag of the test's own.
func newFixture(t *testing.T, check, postsubmit, attempt int) *fixture {
	t.Helper()
	t.Parallel()
	dir := t.TempDir()
	f := &fixture{
		t:       t,
		slice:   "crate-t" + randomHex(t),
		tag:     randomHex(t),
		state:   filepath.Join(dir, "state"),
		machine: filepath.Join(dir, "machine.toml"),
	}
	if err := os.MkdirAll(f.state, 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("[count]\ncheck = %d\npostsubmit = %d\nattempt = %d\n", check, postsubmit, attempt)
	if err := os.WriteFile(f.machine, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	f.env = append(os.Environ(),
		"CRATE_MACHINE="+f.machine,
		"CRATE_SLICE="+f.slice,
		"XDG_STATE_HOME="+f.state,
	)
	t.Cleanup(f.cleanup)
	return f
}

func randomHex(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// system is a system name of the test's own, so no two tests share a slot or a network.
func (f *fixture) system(label string) string { return "sys-" + label + "-" + f.tag }

// plainSystem is a system name with nothing systemd escapes, so its slice's path is its name.
func (f *fixture) plainSystem(label string) string { return "sys" + label + f.tag }

// absent is a path that is never there, for a holder that waits until the test kills it.
func (f *fixture) absent() string { return filepath.Join(f.t.TempDir(), "never") }

func (f *fixture) cleanup() {
	for _, c := range f.containers {
		_, _ = exec.Command("podman", "rm", "-f", c).CombinedOutput()
	}
	for _, n := range f.networks {
		_, _ = exec.Command("podman", "network", "rm", "-f", n).CombinedOutput()
	}
	f.stopSlices()
}

// stopSlices stops every slice this test made, deepest first: each leaf, its kind's slice and the machine's
// own. A unit that a helper process made is found by its name's prefix, so a holder cannot leave one behind.
func (f *fixture) stopSlices() {
	out, _ := exec.Command("systemctl", "--user", "list-units", "--type=slice", "--all",
		"--plain", "--no-legend", "--no-pager", "--full").CombinedOutput()
	var units []string
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if name := fields[0]; strings.HasPrefix(name, f.slice) && strings.HasSuffix(name, ".slice") {
			units = append(units, name)
		}
	}
	units = append(units, f.slice+".slice")
	sort.Slice(units, func(i, j int) bool { return len(units[i]) > len(units[j]) })
	seen := map[string]bool{}
	for _, unit := range units {
		if seen[unit] {
			continue
		}
		seen[unit] = true
		_, _ = exec.Command("systemctl", "--user", "stop", unit).CombinedOutput()
	}
	for unit := range seen {
		_, _ = exec.Command("systemctl", "--user", "reset-failed", unit).CombinedOutput()
	}
}

// run runs a command and fails the test with its output when it fails.
func (f *fixture) run(name string, args ...string) string {
	f.t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		f.t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// place asks the machine for a placement, through a process of its own, and fails when it gives none.
func (f *fixture) place(kind, system string, extra ...string) machine.Placement {
	f.t.Helper()
	h := f.startHelper("place", kind, system, extra...)
	out := h.waitOut(f.t, 30*time.Second)
	h.waitExit(f.t, 30*time.Second)
	placement, ok := strings.CutPrefix(out, "ok ")
	if !ok {
		f.t.Fatalf("Place(%q, %q): %q", kind, system, out)
	}
	return machine.Placement(placement)
}

// start runs a container with `--cgroup-parent=` the placement the machine gives, and returns its name.
func (f *fixture) start(kind, system string, opts, command []string) (string, machine.Placement) {
	f.t.Helper()
	leaf := f.place(kind, system)
	name := "crate-c-" + randomHex(f.t)
	args := []string{"run", "-d", "--name", name, "--cgroup-parent=" + string(leaf)}
	args = append(args, opts...)
	args = append(args, testImage)
	args = append(args, command...)
	f.run("podman", args...)
	f.containers = append(f.containers, name)
	return name, leaf
}

// burn is a container command with more threads than the machine has CPUs.
func burn(threads int) []string {
	return []string{"sh", "-c", fmt.Sprintf(
		"i=0; while [ $i -lt %d ]; do (while :; do :; done) & i=$((i+1)); done; wait", threads)}
}

func (f *fixture) pid(name string) int {
	f.t.Helper()
	n, err := strconv.Atoi(f.run("podman", "inspect", "-f", "{{.State.Pid}}", name))
	if err != nil {
		f.t.Fatalf("the pid of %s: %v", name, err)
	}
	return n
}

// cgroup is the host cgroup folder of a running container.
func (f *fixture) cgroup(name string) string {
	f.t.Helper()
	return cgroupOf(f.t, f.pid(name))
}

func cgroupOf(t *testing.T, pid int) string {
	t.Helper()
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		t.Fatalf("the cgroup of pid %d: %v", pid, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "0::") {
			return "/sys/fs/cgroup" + strings.TrimPrefix(line, "0::")
		}
	}
	t.Fatalf("pid %d has no cgroup v2 line: %q", pid, data)
	return ""
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

// cpu is a container's CPU time used, in microseconds, from cpu.stat.
func (f *fixture) cpu(name string) int64 {
	f.t.Helper()
	return cpuAt(f.t, f.cgroup(name))
}

func cpuAt(t *testing.T, dir string) int64 {
	t.Helper()
	for _, line := range strings.Split(readFile(t, filepath.Join(dir, "cpu.stat")), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "usage_usec" {
			n, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				t.Fatalf("cpu.stat usage_usec %q: %v", fields[1], err)
			}
			return n
		}
	}
	t.Fatalf("cpu.stat has no usage_usec")
	return 0
}

func (f *fixture) running(name string) bool {
	f.t.Helper()
	return f.run("podman", "inspect", "-f", "{{.State.Running}}", name) == "true"
}

// show reads properties of a user unit.
func (f *fixture) show(unit string, props ...string) map[string]string {
	f.t.Helper()
	args := []string{"--user", "show", unit}
	for _, p := range props {
		args = append(args, "-p", p)
	}
	out := f.run("systemctl", args...)
	got := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			got[key] = value
		}
	}
	return got
}

// slotDir is where the machine keeps the slots of a kind.
func (f *fixture) slotDir(kind string) string {
	return filepath.Join(f.state, "crate", "slots", kind)
}

func (f *fixture) slotFiles(kind string) []string {
	f.t.Helper()
	entries, err := os.ReadDir(f.slotDir(kind))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		f.t.Fatalf("reading %s: %v", f.slotDir(kind), err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			files = append(files, filepath.Join(f.slotDir(kind), e.Name()))
		}
	}
	return files
}

func (f *fixture) oneSlot(kind string) string {
	f.t.Helper()
	files := f.slotFiles(kind)
	if len(files) != 1 {
		f.t.Fatalf("the %s slots: expected one file, got %v", kind, files)
	}
	return files[0]
}

// pathWith puts stand-in programs first on PATH for a helper process.
func (f *fixture) pathWith(scripts map[string]string) []string {
	f.t.Helper()
	dir := f.t.TempDir()
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			f.t.Fatal(err)
		}
	}
	return []string{"PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH")}
}

// --- the helper process ---

type helperProc struct {
	cmd     *exec.Cmd
	out     string
	errFile string
	done    chan struct{}
}

func (f *fixture) startHelper(mode, kind, system string, extra ...string) *helperProc {
	f.t.Helper()
	return f.startHelperEnv(f.env, mode, kind, system, extra...)
}

// hookHelper is a holder with the environment a git hook's process has: no runtime folder and no session bus.
func (f *fixture) hookHelper(mode, kind, system string, extra ...string) *helperProc {
	f.t.Helper()
	var base []string
	for _, e := range f.env {
		if strings.HasPrefix(e, "XDG_RUNTIME_DIR=") || strings.HasPrefix(e, "DBUS_SESSION_BUS_ADDRESS=") {
			continue
		}
		base = append(base, e)
	}
	for _, e := range extra {
		if strings.HasPrefix(e, "XDG_RUNTIME_DIR=") || strings.HasPrefix(e, "DBUS_SESSION_BUS_ADDRESS=") {
			f.t.Fatalf("a hook's environment has no %s", e)
		}
	}
	return f.startHelperEnv(base, mode, kind, system, extra...)
}

func (f *fixture) startHelperEnv(baseEnv []string, mode, kind, system string, extra ...string) *helperProc {
	f.t.Helper()
	dir := f.t.TempDir()
	h := &helperProc{
		out:     filepath.Join(dir, "out"),
		errFile: filepath.Join(dir, "err"),
		done:    make(chan struct{}),
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	errFile, err := os.Create(h.errFile)
	if err != nil {
		f.t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = errFile, errFile
	base := []string{
		helperMarker + "=1",
		"HELPER_MODE=" + mode,
		"HELPER_KIND=" + kind,
		"HELPER_SYSTEM=" + system,
		"HELPER_OUT=" + h.out,
	}
	cmd.Env = append(baseEnv, append(base, extra...)...)
	if err := cmd.Start(); err != nil {
		errFile.Close()
		f.t.Fatal(err)
	}
	h.cmd = cmd
	go func() {
		_ = cmd.Wait()
		_ = errFile.Close()
		close(h.done)
	}()
	f.t.Cleanup(func() {
		select {
		case <-h.done:
			return
		default:
		}
		_ = cmd.Process.Kill()
		select {
		case <-h.done:
		case <-time.After(5 * time.Second):
			f.t.Logf("the holder pid %d did not exit after a kill", cmd.Process.Pid)
		}
	})
	return h
}

func (h *helperProc) stderr() string {
	data, _ := os.ReadFile(h.errFile)
	return string(data)
}

func (h *helperProc) waitOut(t *testing.T, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(h.out); err == nil {
			return strings.TrimSpace(string(data))
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the holder wrote no answer within %v; its stderr: %q", timeout, h.stderr())
	return ""
}

func (h *helperProc) waitExit(t *testing.T, timeout time.Duration) {
	t.Helper()
	select {
	case <-h.done:
	case <-time.After(timeout):
		t.Fatalf("the holder did not exit within %v; its stderr: %q", timeout, h.stderr())
	}
}

// outTime is the moment a holder wrote its answer: `ok <nanos>`.
func outTime(t *testing.T, out string) time.Time {
	t.Helper()
	field, ok := strings.CutPrefix(out, "ok ")
	if !ok {
		t.Fatalf("the holder's answer: expected `ok <nanos>`, got %q", out)
	}
	n, err := strconv.ParseInt(field, 10, 64)
	if err != nil {
		t.Fatalf("the holder's answer %q: %v", out, err)
	}
	return time.Unix(0, n)
}

// outMillis is how long a holder took at its own work: `ok <millis>`.
func outMillis(t *testing.T, out string) int64 {
	t.Helper()
	field, ok := strings.CutPrefix(out, "ok ")
	if !ok {
		t.Fatalf("the holder's answer: expected `ok <millis>`, got %q", out)
	}
	n, err := strconv.ParseInt(field, 10, 64)
	if err != nil {
		t.Fatalf("the holder's answer %q: %v", out, err)
	}
	return n
}

// noneOut is a holder that placed once and held three times: `ok <millis> place="<placement>"`.
func noneOut(t *testing.T, out string) (int64, machine.Placement) {
	t.Helper()
	fields := strings.Fields(out)
	if len(fields) != 3 || fields[0] != "ok" {
		t.Fatalf("the holder's answer: expected `ok <millis> place=<placement>`, got %q", out)
	}
	n, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		t.Fatalf("the holder's answer %q: %v", out, err)
	}
	placement, ok := strings.CutPrefix(fields[2], `place="`)
	if !ok || !strings.HasSuffix(placement, `"`) {
		t.Fatalf("the holder's answer %q: expected a place=<placement>", out)
	}
	return n, machine.Placement(strings.TrimSuffix(placement, `"`))
}

// requireHeld says a holder's answer is a hold, and fails the test when it is an error.
func requireHeld(t *testing.T, out, what string) {
	t.Helper()
	if !strings.HasPrefix(out, "ok ") {
		t.Fatalf("%s: expected it to hold, got %q", what, out)
	}
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestHelperProcess is the body of a holder the tests start. It does nothing when the test runner calls it.
func TestHelperProcess(t *testing.T) {
	if os.Getenv(helperMarker) == "" {
		return
	}
	mode := os.Getenv("HELPER_MODE")
	kind := os.Getenv("HELPER_KIND")
	system := os.Getenv("HELPER_SYSTEM")
	out := os.Getenv("HELPER_OUT")
	write := func(text string) { _ = os.WriteFile(out, []byte(text), 0o644) }
	wait := func(path string) {
		timeout := 60 * time.Second
		if v := os.Getenv("HELPER_TIMEOUT"); v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				timeout = d
			}
		}
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if path == "" {
				time.Sleep(timeout)
				return
			}
			if _, err := os.Stat(path); err == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	fail := func(err error) { write("err " + err.Error()); os.Exit(0) }
	network := os.Getenv("HELPER_NETWORK")
	m := machine.Default()
	switch mode {
	case "place":
		p, err := m.Place(kind, system)
		if err != nil {
			fail(err)
		} else {
			write("ok " + string(p))
		}
	case "unplace":
		if err := m.Unplace(kind, system); err != nil {
			fail(err)
		} else {
			write("ok ")
		}
	case "hold":
		s, err := m.Hold(kind, system)
		if err != nil {
			fail(err)
			break
		}
		write(fmt.Sprintf("ok %d", time.Now().UnixNano()))
		wait(os.Getenv("HELPER_RELEASE"))
		s.Release()
	case "hold-bind":
		s, err := m.Hold(kind, system)
		if err != nil {
			fail(err)
			break
		}
		if out, err := exec.Command("podman", "network", "create", network).CombinedOutput(); err != nil {
			fail(fmt.Errorf("network create: %v\n%s", err, out))
			break
		}
		s.Bind()
		write(fmt.Sprintf("ok %d", time.Now().UnixNano()))
		wait(os.Getenv("HELPER_RELEASE"))
		s.Release()
	case "hold-bind-exit":
		s, err := m.Hold(kind, system)
		if err != nil {
			fail(err)
			break
		}
		if out, err := exec.Command("podman", "network", "create", network).CombinedOutput(); err != nil {
			fail(fmt.Errorf("network create: %v\n%s", err, out))
			break
		}
		s.Bind()
		write(fmt.Sprintf("ok %d", time.Now().UnixNano()))
	case "one":
		started := time.Now()
		first, err := m.Hold(kind, system)
		if err != nil {
			fail(err)
			break
		}
		for _, k := range []string{machine.Work, machine.Check, machine.Work} {
			if _, err := m.Place(k, system); err != nil {
				fail(err)
				break
			}
		}
		second, err := m.Hold(kind, system)
		if err != nil {
			fail(err)
			break
		}
		write(fmt.Sprintf("ok %d", time.Since(started).Milliseconds()))
		wait(os.Getenv("HELPER_RELEASE"))
		first.Release()
		second.Release()
	case "work-holds":
		started := time.Now()
		var slots []machine.Slot
		for _, sys := range strings.Split(os.Getenv("HELPER_SYSTEMS"), ",") {
			s, err := m.Hold(machine.Work, sys)
			if err != nil {
				fail(err)
				break
			}
			slots = append(slots, s)
		}
		write(fmt.Sprintf("ok %d", time.Since(started).Milliseconds()))
		wait(os.Getenv("HELPER_RELEASE"))
		for _, s := range slots {
			s.Release()
		}
	case "none":
		started := time.Now()
		p, err := m.Place(kind, system)
		if err != nil {
			fail(err)
			break
		}
		for _, sys := range strings.Split(os.Getenv("HELPER_SYSTEMS"), ",") {
			if _, err := m.Hold(machine.Check, sys); err != nil {
				fail(err)
				break
			}
		}
		write(fmt.Sprintf("ok %d place=%q", time.Since(started).Milliseconds(), string(p)))
	}
	os.Exit(0)
}
