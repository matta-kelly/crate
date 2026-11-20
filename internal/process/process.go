// Package process is the one way crate runs another program. A call runs in a process group of its own, so what it
// starts goes with it. At its limit, or when its context ends, the whole group gets TERM, and KILL after limit.Grace,
// and Run returns only after that: a stopped call never leaves a process behind, and never holds its caller.
package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/matta-kelly/crate/internal/limit"
)

// Call is one run of a program.
type Call struct {
	Name   string        // what its error calls it; "" for the program and its first argument
	Prog   string        // the program, found on PATH
	Args   []string      // its arguments
	Env    []string      // its env; nil for the caller's own
	Dir    string        // its folder; "" for the caller's
	Limit  time.Duration // the longest it may run; 0 for no limit of its own, so the context alone ends it
	Stdin  io.Reader     // nil for none
	Stdout io.Writer     // nil drops it
	Stderr io.Writer     // nil drops it
	// Foreground is the caller's own command, as crate run's: it stays in the caller's process group, so a signal
	// from the terminal reaches it, and only it gets TERM.
	Foreground bool
}

// Error is a call that did not pass: it exited non-zero, ran past its limit, was stopped when its context ended, or
// did not start.
type Error struct {
	Name    string
	Code    int           // its exit code; -1 when it did not exit by itself
	Message string        // its own message, from its stderr when Output kept it
	Limit   time.Duration // its limit, when it ran past it
	Stopped error         // the context's error, when the context ended first
	err     error
}

func (e *Error) Error() string {
	switch {
	case e.Stopped != nil:
		return fmt.Sprintf("%s was stopped: %v", e.Name, e.Stopped)
	case e.Limit > 0:
		return fmt.Sprintf("%s did not finish within %v", e.Name, e.Limit)
	case e.Message != "":
		return fmt.Sprintf("%s: %s", e.Name, e.Message)
	}
	return fmt.Sprintf("%s: %v", e.Name, e.err)
}

// Unwrap is the context's error for a stopped call, else the program's own.
func (e *Error) Unwrap() error {
	if e.Stopped != nil {
		return e.Stopped
	}
	return e.err
}

// ExitCode is the exit code of a call that exited by itself: 0 for nil, -1 for one that did not exit by itself.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var failed *Error
	if errors.As(err, &failed) {
		return failed.Code
	}
	return -1
}

// Run runs the call, its output to its writers.
func Run(ctx context.Context, c Call) error {
	name := c.Name
	if name == "" {
		name = c.Prog
		if len(c.Args) > 0 {
			name += " " + c.Args[0]
		}
	}
	callCtx := ctx
	if c.Limit > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, c.Limit)
		defer cancel()
	}
	cmd := exec.CommandContext(callCtx, c.Prog, c.Args...)
	cmd.Env, cmd.Dir = c.Env, c.Dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = c.Stdin, c.Stdout, c.Stderr
	if !c.Foreground {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	var stopped time.Time
	cmd.Cancel = func() error {
		stopped = time.Now()
		if c.Foreground {
			_ = cmd.Process.Signal(syscall.SIGTERM)
		} else {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		}
		return nil
	}
	// Past the grace, Wait kills the program itself and closes its pipes, so a process that keeps them never holds it.
	cmd.WaitDelay = limit.Grace()
	err := cmd.Run()
	if !stopped.IsZero() && !c.Foreground {
		sweep(cmd.Process.Pid, stopped.Add(limit.Grace()))
	}
	if err == nil {
		return nil
	}
	failed := &Error{Name: name, Code: -1, err: err}
	var exit *exec.ExitError
	switch {
	case ctx.Err() != nil:
		failed.Stopped = ctx.Err()
	case callCtx.Err() != nil:
		failed.Limit = c.Limit
	case errors.As(err, &exit) && exit.Exited():
		failed.Code = exit.ExitCode()
	}
	return failed
}

// Output runs the call and gives its stdout, trimmed. Its stderr goes to c.Stderr too, and is the error's message, so a
// failure says what the program said.
func Output(ctx context.Context, c Call) (string, error) {
	var out, errb bytes.Buffer
	stdout := io.Writer(&out)
	if c.Stdout != nil {
		stdout = io.MultiWriter(&out, c.Stdout)
	}
	stderr := io.Writer(&errb)
	if c.Stderr != nil {
		stderr = io.MultiWriter(&errb, c.Stderr)
	}
	c.Stdout, c.Stderr = stdout, stderr
	err := Run(ctx, c)
	var failed *Error
	if errors.As(err, &failed) {
		failed.Message = strings.TrimSpace(errb.String())
	}
	return strings.TrimSpace(out.String()), err
}

// Combined runs the call and gives its stdout and stderr as one text, trimmed, which is also the error's message.
func Combined(ctx context.Context, c Call) (string, error) {
	var out bytes.Buffer
	both := io.Writer(&out)
	if c.Stdout != nil {
		both = io.MultiWriter(&out, c.Stdout)
	}
	c.Stdout, c.Stderr = both, both
	err := Run(ctx, c)
	text := strings.TrimSpace(out.String())
	var failed *Error
	if errors.As(err, &failed) {
		failed.Message = text
	}
	return text, err
}

// sweep waits until the group is empty or the deadline passes, then kills what is left of it.
func sweep(group int, deadline time.Time) {
	for time.Now().Before(deadline) {
		if syscall.Kill(-group, 0) != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(-group, syscall.SIGKILL)
}
