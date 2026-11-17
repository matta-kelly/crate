package machine_test

// The bound: crate draws a line under all its work on crate.slice (its memory line and its weights), every
// controller the line needs is there, a call through a placement runs in the slice, and a heavy writer in the slice
// keeps its unwritten data to the line, on this machine's own disk.

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matta-kelly/crate/internal/machine"
)

// memTotal is this machine's memory in bytes.
func memTotal(t *testing.T) uint64 {
	t.Helper()
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if fields := strings.Fields(scanner.Text()); len(fields) == 3 && fields[0] == "MemTotal:" {
			kb, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return kb * 1024
		}
	}
	t.Fatal("no MemTotal in /proc/meminfo")
	return 0
}

// After a Place, crate's own slice has its memory line, a quarter of the machine's memory when the file names none, its
// weights below a sibling's 100, and the cpu, io and memory controllers.
func TestPlaceDrawsCratesLineUnderAllItsWork(t *testing.T) {
	f := newFixture(t, 1, 1, 1)
	f.place(machine.Work, "que-pasa-main")
	unit := f.slice + ".slice"
	props := f.show(unit, "MemoryHigh", "CPUWeight", "IOWeight", "ControlGroup")
	if want := strconv.FormatUint(memTotal(t)*25/100, 10); props["MemoryHigh"] != want {
		t.Errorf("%s MemoryHigh: expected a quarter of the machine's memory, %s, got %s", unit, want, props["MemoryHigh"])
	}
	for key, want := range map[string]string{"CPUWeight": "50", "IOWeight": "50"} {
		if props[key] != want {
			t.Errorf("%s %s: expected %s, got %s", unit, key, want, props[key])
		}
	}
	data, err := os.ReadFile(filepath.Join("/sys/fs/cgroup", props["ControlGroup"], "cgroup.controllers"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"cpu", "io", "memory"} {
		if !strings.Contains(" "+strings.TrimSpace(string(data))+" ", " "+c+" ") {
			t.Errorf("%s's controllers: expected %s, got %q", unit, c, data)
		}
	}
}

// The machine file's [slice] memory is the line.
func TestTheMachineFilesLineIsTheSlicesLine(t *testing.T) {
	f := newFixture(t, 1, 1, 1)
	file := f.machineWith("[slice]\nmemory = \"300M\"\n")
	f.place(machine.Work, "que-pasa-main", "CRATE_MACHINE="+file)
	if got := f.show(f.slice+".slice", "MemoryHigh")["MemoryHigh"]; got != strconv.Itoa(300<<20) {
		t.Errorf("MemoryHigh: expected 300M, %d, got %s", 300<<20, got)
	}
}

// A call through a placement runs in the slice, with its output and its exit code the caller's.
func TestACallThroughAPlacementRunsInTheSlice(t *testing.T) {
	f := newFixture(t, 1, 1, 1)
	leaf := f.place(machine.Work, "que-pasa-main")
	prog, args, env := leaf.Command("sh", "-c", "cat /proc/self/cgroup; exit 3")
	cmd := exec.Command(prog, args...)
	cmd.Env = env
	out, err := cmd.Output()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 3 {
		t.Fatalf("the call's exit: expected 3, got %v", err)
	}
	if !strings.Contains(string(out), "/"+string(leaf)+"/") {
		t.Errorf("the call's cgroup: expected it under %s, got %q", leaf, out)
	}
	if prog, args, env := machine.Placement("").Command("sh", "-c", "true"); prog != "sh" || len(args) != 2 || env != nil {
		t.Errorf("no placement: expected the call as it is, got %s %v %v", prog, args, env)
	}
}

// A container in the slice that writes 1.5G to this machine's disk keeps its unwritten data under its 512M line (about
// 0.6 of it, measured): the kernel slows the writer in place, and the write still finishes. Without the line it would
// pile up most of the file, since the kernel's own limit is a share of all the machine's memory.
func TestAWriterInTheSliceKeepsItsUnwrittenDataToTheLine(t *testing.T) {
	f := newFixture(t, 1, 1, 1)
	file := f.machineWith("[slice]\nmemory = \"512M\"\n")
	leaf := f.place(machine.Work, f.plainSystem("w"), "CRATE_MACHINE="+file)
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	// On the machine's disk, not t.TempDir: /tmp is memory, whose pages are never written out.
	dir, err := os.MkdirTemp(cache, "crate-bound-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	stat := filepath.Join("/sys/fs/cgroup", f.show(f.slice+".slice", "ControlGroup")["ControlGroup"], "memory.stat")

	name := "crate-c-" + randomHex(t)
	f.containers = append(f.containers, name)
	prog, args, env := leaf.Command("podman", "run", "--rm", "--name", name, "--cgroup-parent="+string(leaf),
		"-v", dir+":/data:Z", testImage, "dd", "if=/dev/zero", "of=/data/f", "bs=1M", "count=1536")
	cmd := exec.Command(prog, args...)
	cmd.Env = env
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var most uint64
	for running := true; running; {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("the write: %v\n%s", err, out.String())
			}
			running = false
		case <-time.After(50 * time.Millisecond):
			if dirty := fileDirty(t, stat); dirty > most {
				most = dirty
			}
		}
	}
	if info, err := os.Stat(filepath.Join(dir, "f")); err != nil || info.Size() != 1536<<20 {
		t.Fatalf("the write: expected 1536M on disk, got %v, %v", info, err)
	}
	if most == 0 || most >= 512<<20 {
		t.Errorf("the slice's unwritten data: expected some, and never up to its 512M line, got a peak of %dM", most>>20)
	}
	t.Logf("the slice's unwritten data peaked at %dM under its 512M line", most>>20)
}

// fileDirty is a cgroup's unwritten file data, from its memory.stat.
func fileDirty(t *testing.T, stat string) uint64 {
	t.Helper()
	data, err := os.ReadFile(stat)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "file_dirty "); ok {
			n, err := strconv.ParseUint(value, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
	}
	t.Fatalf("%s has no file_dirty", stat)
	return 0
}

// machineWith is a machine file of this test's counts and the given lines.
func (f *fixture) machineWith(lines string) string {
	f.t.Helper()
	path := filepath.Join(f.t.TempDir(), "machine.toml")
	if err := os.WriteFile(path, []byte(fmt.Sprintf("[count]\ncheck = 1\npostsubmit = 1\nattempt = 1\n\n%s", lines)), 0o644); err != nil {
		f.t.Fatal(err)
	}
	return path
}
