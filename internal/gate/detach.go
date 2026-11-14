package gate

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// StartWorker starts the repo's worker, crate queue run <bare>, when changes wait and none runs. Its output goes to
// worker.log.
func (r *Repo) StartWorker() error {
	if q, err := r.readQueue(); err != nil || len(q) == 0 {
		return err
	}
	unlock, free := r.tryLock("worker.lock")
	if !free {
		return nil
	}
	unlock()
	return r.detach("worker.log", "queue", r.Bare, "run")
}

// startPostsubmit starts crate postsubmit <bare>. Its output goes to postsubmit/run.log.
func (r *Repo) startPostsubmit() error {
	if err := os.MkdirAll(r.state("postsubmit"), 0o755); err != nil {
		return err
	}
	return r.detach("postsubmit/run.log", "postsubmit", r.Bare)
}

// detach starts crate with args in its own session, with no tie to the hook that starts it, so a push does not wait
// for it and a closed connection does not stop it. Its output is appended to the state file log.
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
	cmd := exec.Command(self, args...)
	cmd.Dir = r.Bare
	cmd.Stdout, cmd.Stderr = f, f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
