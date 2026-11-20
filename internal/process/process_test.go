package process_test

// The runner: a call's whole process group gets TERM at its limit or when its context ends, and KILL after the grace;
// Run returns only after that.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/matta-kelly/crate/internal/process"
)

// gone says the process is no longer running: there is none, or it is a zombie its new parent has not reaped yet.
func gone(pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	fields := strings.Fields(string(data[strings.LastIndexByte(string(data), ')')+1:]))
	return len(fields) > 0 && fields[0] == "Z"
}

func readPid(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no pid in %s", path)
	return 0
}

// A call past its limit: its group gets TERM, which a shell that traps it notes, then KILL after the grace, which
// takes the child that ignores TERM. Run returns after the grace, not before, and names the limit.
func TestACallPastItsLimitGetsTermThenKillOnItsWholeGroup(t *testing.T) {
	t.Setenv("CRATE_LIMIT_SCALE", "3600") // the grace is 1 s
	dir := t.TempDir()
	child, termed := filepath.Join(dir, "child"), filepath.Join(dir, "termed")
	script := `(trap "" TERM; exec sleep 60) & echo $! >` + child + `
trap 'echo term >` + termed + `' TERM
while :; do sleep 0.1; done`
	started := time.Now()
	err := process.Run(context.Background(), process.Call{Name: "the stubborn call", Prog: "sh", Args: []string{"-c", script},
		Limit: time.Second})
	took := time.Since(started)

	var failed *process.Error
	if !errors.As(err, &failed) || failed.Limit != time.Second {
		t.Fatalf("expected an error that names the limit, got %v", err)
	}
	if !strings.Contains(err.Error(), "the stubborn call did not finish within 1s") {
		t.Errorf("expected the error to name the call and its limit, got %q", err)
	}
	if _, err := os.Stat(termed); err != nil {
		t.Errorf("expected the shell to get TERM first, got no mark: %v", err)
	}
	if took < 1900*time.Millisecond || took > 6*time.Second {
		t.Errorf("expected Run to return after the limit and the grace (about 2 s), took %v", took)
	}
	pid := readPid(t, child)
	deadline := time.Now().Add(2 * time.Second)
	for !gone(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !gone(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Errorf("expected the child that ignores TERM to be killed with its group, pid %d still runs", pid)
	}
}

// A group that goes at TERM is not held for the grace.
func TestAGroupThatGoesAtTermIsNotHeldForTheGrace(t *testing.T) {
	t.Setenv("CRATE_LIMIT_SCALE", "")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	started := time.Now()
	err := process.Run(ctx, process.Call{Prog: "sh", Args: []string{"-c", "sleep 60 & wait"}})
	if took := time.Since(started); took > 3*time.Second {
		t.Errorf("expected Run to return soon after TERM, took %v (the grace is 10 s)", took)
	}
	var failed *process.Error
	if !errors.As(err, &failed) || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected a stopped call, got %v", err)
	}
}

// A call that exits non-zero gives its code, and Output keeps its stderr as the message.
func TestAFailedCallGivesItsCodeAndItsMessage(t *testing.T) {
	out, err := process.Output(context.Background(), process.Call{Prog: "sh", Args: []string{"-c", "echo out; echo why >&2; exit 3"}})
	if out != "out" {
		t.Errorf("expected stdout %q, got %q", "out", out)
	}
	if code := process.ExitCode(err); code != 3 {
		t.Errorf("expected exit code 3, got %d (%v)", code, err)
	}
	if err == nil || err.Error() != "sh -c: why" {
		t.Errorf("expected the error %q, got %v", "sh -c: why", err)
	}
	if code := process.ExitCode(nil); code != 0 {
		t.Errorf("expected 0 for no error, got %d", code)
	}
}

// A process that keeps the call's output open after the call exits never holds Run past the grace.
func TestAnOpenPipeDoesNotHoldACallPastTheGrace(t *testing.T) {
	t.Setenv("CRATE_LIMIT_SCALE", "3600")
	started := time.Now()
	_, _ = process.Output(context.Background(), process.Call{Prog: "sh", Args: []string{"-c", "sleep 60 & exit 0"},
		Limit: time.Second})
	if took := time.Since(started); took > 6*time.Second {
		t.Errorf("expected Run to return by the limit and the grace, took %v", took)
	}
}
