package gate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"github.com/matta-kelly/crate/internal/limit"
	"github.com/matta-kelly/crate/internal/machine"
	"github.com/matta-kelly/crate/internal/process"
)

// What StartWorker did for the worker.
const (
	WorkerIdle    = "idle"    // no change waits
	WorkerStarted = "started" // it started one
	WorkerRunning = "running" // one runs already
	WorkerPaused  = "paused"  // the machine is paused: none starts until crate apply finds it well
)

// StartWorker starts the repo's worker, crate queue run <bare>, when changes wait, none runs and the machine is not
// paused, and says which it found. Its output goes to worker.log. It also starts the publish of a green main not yet
// published (StartShip), the dev image cache's pending work (StartCache), and a postsubmit that crate could not run
// (startRetry), so none of them, when it cannot start, ever holds the queue.
func (r *Repo) StartWorker() (string, error) {
	var ship, cache error
	if err := r.StartShip(); err != nil {
		ship = fmt.Errorf("the publish did not start: %w", err)
	}
	if err := r.StartCache(); err != nil {
		cache = fmt.Errorf("the dev image cache did not start: %w", err)
	}
	var retry error
	if err := r.startRetry(); err != nil {
		retry = fmt.Errorf("the postsubmit to run again did not start: %w", err)
	}
	state, err := r.startWorker()
	var worker error
	if err != nil {
		worker = fmt.Errorf("its worker did not start: %w", err)
	}
	return state, errors.Join(worker, ship, cache, retry)
}

func (r *Repo) startWorker() (string, error) {
	q, err := r.readQueue()
	if err != nil {
		return "", err
	}
	if len(q) == 0 {
		return WorkerIdle, nil
	}
	h, err := machine.ReadHealth()
	if err != nil {
		return "", err
	}
	if h.Paused != "" {
		return WorkerPaused, nil
	}
	unlock, free, err := r.tryLock("worker.lock")
	if err != nil {
		return "", err
	}
	if !free {
		return WorkerRunning, nil
	}
	unlock()
	return WorkerStarted, r.detach("worker.log", "queue", r.Bare, "run")
}

// startPostsubmit starts crate postsubmit <bare>. Its output goes to postsubmit/run.log.
func (r *Repo) startPostsubmit() error {
	if err := os.MkdirAll(r.state("postsubmit"), 0o755); err != nil {
		return err
	}
	return r.detach("postsubmit/run.log", "postsubmit", r.Bare)
}

// detach starts crate with args in its own session and its own scope (machine.Own), with no tie to the hook or unit
// that starts it, so a push does not wait for it, and neither a closed connection nor the end of a oneshot unit stops
// it. Its output is appended to the state file log.
func (r *Repo) detach(log string, args ...string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(r.state(log), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	// git hands its hooks more open files than stdin, stdout and stderr, and waits for the push until every copy of
	// them is closed: the child must not keep one.
	if fds, err := os.ReadDir("/proc/self/fd"); err == nil {
		for _, fd := range fds {
			if n, err := strconv.Atoi(fd.Name()); err == nil && n > 2 {
				syscall.CloseOnExec(n)
			}
		}
	}
	// In a scope of crate's own slice, so a oneshot unit that started it (a timer's crate apply) does not take it down
	// when the unit ends, and it is under crate's line. The scope is proven first, with a call that waits: a detached
	// child that fails cannot say so, so a manager that does not answer fails here, loud, and never falls back to a
	// child the unit's end would kill.
	own := machine.Own()
	if own != "" {
		prog, argv, env := own.Command("true")
		if _, err := process.Combined(context.Background(), process.Call{Prog: prog, Args: argv, Env: env,
			Limit: limit.Systemd()}); err != nil {
			return fmt.Errorf("crate's own slice %s: the user's manager did not make a scope (it is found at $XDG_RUNTIME_DIR/bus): %w",
				own, err)
		}
	}
	prog, argv, env := own.Command(self, args...)
	cmd := exec.Command(prog, argv...)
	cmd.Env = env
	cmd.Dir = r.Bare
	cmd.Stdout, cmd.Stderr = f, f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
