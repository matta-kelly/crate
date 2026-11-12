package machine_test

// The fixture of the machine tests: a machine file, a slice name and a state folder of the test's own, on
// real podman and the user's systemd. A test starts containers through the placement `machine.Default()`
// gives and cleans up its units, containers and networks.

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

// helperMarker is set only in a process the test starts to hold a slot, so TestHelperProcess returns at once
// when the test runner calls it as an ordinary test.
const helperMarker = "CRATE_MACHINE_TEST_HELPER"

type fixture struct {
	t          *testing.T
	slice      string
	tag        string
	state      string
	machine    string
	containers []string
	networks   []string
}

// newFixture writes a machine file and names a slice, a state folder and a tag of the test's own.
func newFixture(t *testing.T, cpus, memory string, check, postsubmit, attempt int) *fixture {
	t.Helper()
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
	body := fmt.Sprintf("cpus = %q\nmemory = %q\n\n[count]\ncheck = %d\npostsubmit = %d\nattempt = %d\n",
		cpus, memory, check, postsubmit, attempt)
	if err := os.WriteFile(f.machine, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CRATE_MACHINE", f.machine)
	t.Setenv("CRATE_SLICE", f.slice)
	t.Setenv("XDG_STATE_HOME", f.state)
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

// place asks the machine for a placement and fails when it gives none: every fixture declares a ceiling.
func (f *fixture) place(kind, system string) machine.Placement {
	f.t.Helper()
	p, err := machine.Default().Place(kind, system)
	if err != nil {
		f.t.Fatalf("Place(%q, %q): %v", kind, system, err)
	}
	if p == "" {
		f.t.Fatalf("Place(%q, %q) gave no slice, but the machine file declares a ceiling", kind, system)
	}
	return p
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

// burn returns a container command with more threads than the CPU set of the fixture (two CPUs).
func burn() []string {
	return []string{"sh", "-c", "i=0; while [ $i -lt 8 ]; do (while :; do :; done) & i=$((i+1)); done; wait"}
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

// cgroupField is one small file of a cgroup folder, first line only.
func cgroupField(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatalf("reading %s/%s: %v", dir, name, err)
	}
	return strings.TrimSpace(strings.SplitN(string(data), "\n", 2)[0])
}

func (f *fixture) running(name string) bool {
	f.t.Helper()
	return f.run("podman", "inspect", "-f", "{{.State.Running}}", name) == "true"
}

func (f *fixture) oomKilled(name string) bool {
	f.t.Helper()
	return f.run("podman", "inspect", "-f", "{{.State.OOMKilled}}", name) == "true"
}

// base is the cgroup folder of the machine's own slice.
func (f *fixture) base() string {
	f.t.Helper()
	cg := f.run("systemctl", "--user", "show", f.slice+".slice", "-p", "ControlGroup", "--value")
	if cg == "" {
		f.t.Fatalf("the machine's slice %s.slice has no cgroup", f.slice)
	}
	return "/sys/fs/cgroup" + cg
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
		if !e.IsDir() {
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

// pathWith puts stand-in programs first on PATH for this test.
func (f *fixture) pathWith(scripts map[string]string) {
	f.t.Helper()
	dir := f.t.TempDir()
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			f.t.Fatal(err)
		}
	}
	f.t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// --- the holder process ---

type helperProc struct {
	cmd     *exec.Cmd
	out     string
	errFile string
	done    chan struct{}
}

// startHelper runs this test binary as a process that calls the machine, so a slot is held by a process of
// its own, as a system is.
func (f *fixture) startHelper(mode, kind, system string, env ...string) *helperProc {
	f.t.Helper()
	return f.startHelperEnv(os.Environ(), mode, kind, system, env...)
}

// startHelperBare is a holder with an environment of its own, as a git hook's process has: no session bus
// and no runtime folder, unless the test names them.
func (f *fixture) startHelperBare(mode, kind, system string, env ...string) *helperProc {
	f.t.Helper()
	base := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"TMPDIR=" + os.TempDir(),
		"CRATE_MACHINE=" + f.machine,
		"CRATE_SLICE=" + f.slice,
		"XDG_STATE_HOME=" + f.state,
	}
	return f.startHelperEnv(base, mode, kind, system, env...)
}

func (f *fixture) startHelperEnv(baseEnv []string, mode, kind, system string, env ...string) *helperProc {
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
	cmd.Env = append(baseEnv, append(base, env...)...)
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

// startHelperNoSys runs a holder in a mount namespace where /sys is an empty folder, so the CPU list it
// reads cannot be read.
func (f *fixture) startHelperNoSys(mode, kind, system string, env ...string) *helperProc {
	f.t.Helper()
	hidden := f.t.TempDir()
	dir := f.t.TempDir()
	h := &helperProc{
		out:     filepath.Join(dir, "out"),
		errFile: filepath.Join(dir, "err"),
		done:    make(chan struct{}),
	}
	cmd := exec.Command("unshare", "-Urm", "sh", "-c",
		`mount --bind "$1" /sys; shift; exec "$@"`, "sh", hidden, os.Args[0], "-test.run=^TestHelperProcess$")
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
	cmd.Env = append(os.Environ(), append(base, env...)...)
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

// outTime is the moment a holder wrote its answer.
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
	network := os.Getenv("HELPER_NETWORK")
	m := machine.Default()
	switch mode {
	case "place":
		p, err := m.Place(kind, system)
		if err != nil {
			write("err " + err.Error())
		} else {
			write("ok " + string(p))
		}
	case "hold":
		s, err := m.Hold(kind, system)
		if err != nil {
			write("err " + err.Error())
			break
		}
		write(fmt.Sprintf("ok %d", time.Now().UnixNano()))
		wait(os.Getenv("HELPER_RELEASE"))
		s.Release()
	case "hold-bind":
		s, err := m.Hold(kind, system)
		if err != nil {
			write("err " + err.Error())
			break
		}
		if out, err := exec.Command("podman", "network", "create", network).CombinedOutput(); err != nil {
			write(fmt.Sprintf("err network create: %v\n%s", err, out))
			break
		}
		s.Bind()
		write(fmt.Sprintf("ok %d", time.Now().UnixNano()))
		wait(os.Getenv("HELPER_RELEASE"))
		s.Release()
	case "hold-bind-exit":
		s, err := m.Hold(kind, system)
		if err != nil {
			write("err " + err.Error())
			break
		}
		if out, err := exec.Command("podman", "network", "create", network).CombinedOutput(); err != nil {
			write(fmt.Sprintf("err network create: %v\n%s", err, out))
			break
		}
		s.Bind()
		write(fmt.Sprintf("ok %d", time.Now().UnixNano()))
	case "hold-then-bind":
		s, err := m.Hold(kind, system)
		if err != nil {
			write("err " + err.Error())
			break
		}
		write(fmt.Sprintf("ok %d", time.Now().UnixNano()))
		wait(os.Getenv("HELPER_BIND_AT"))
		if out, err := exec.Command("podman", "network", "create", network).CombinedOutput(); err != nil {
			write(fmt.Sprintf("err network create: %v\n%s", err, out))
			break
		}
		s.Bind()
		writeFile(t, os.Getenv("HELPER_BOUND_AT"))
		wait(os.Getenv("HELPER_RELEASE"))
		s.Release()
	}
	os.Exit(0)
}
