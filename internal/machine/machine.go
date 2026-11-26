// Package machine is the seam between crate and the machine it runs on: the slice each kind of crate's work goes in,
// the line crate draws under all of it, and the file that says how many of a kind may run. Here the default machine
// bounds and orders the work in the user's systemd and keeps the slots.
package machine

import (
	"fmt"
	"os"
	"strings"
)

// The four kinds of a claim on the machine.
const (
	Work       = "work"
	Check      = "check"
	Postsubmit = "postsubmit"
	Attempt    = "attempt"
)

// The names crate itself uses for a system. A workspace is never one of them: in a folder of these names it keeps
// its <name>-<folder> (system.Name).
const (
	Router = "router"
	Build  = "build"
)

// CheckKind refuses a kind outside the four.
func CheckKind(kind string) error {
	switch kind {
	case Work, Check, Postsubmit, Attempt:
		return nil
	}
	return fmt.Errorf("machine: kind %q is not one of work, check, postsubmit, attempt", kind)
}

// Placement is a slice name, or "" for none.
type Placement string

// Service is the call that starts prog with args as a transient service of the user's manager in the placement, with
// this process's environment, dir its working folder, and its output appended to log. systemd-run returns once prog
// runs (Type=exec), so a start that fails is the call's error, and from then on prog is the manager's: it outlives the
// caller, its session and its unit. Not for the placement "" (a remote engine's), which has no manager of this
// machine.
func (p Placement) Service(log, dir, prog string, args ...string) (string, []string, []string) {
	argv := []string{"--user", "--quiet", "--collect", "--property=Type=exec", "--slice=" + string(p),
		"--working-directory=" + dir, "--property=StandardOutput=append:" + log, "--property=StandardError=append:" + log}
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); !unitEnv[name] {
			argv = append(argv, "--setenv="+kv)
		}
	}
	argv = append(append(argv, "--", prog), args...)
	return "systemd-run", argv, managerEnv()
}

// unitEnv is what systemd gives a unit's process of its own: a service gets its own anew, not its caller's.
var unitEnv = map[string]bool{"INVOCATION_ID": true, "JOURNAL_STREAM": true, "NOTIFY_SOCKET": true, "LISTEN_PID": true,
	"LISTEN_FDS": true, "LISTEN_FDNAMES": true, "MANAGERPID": true, "SYSTEMD_EXEC_PID": true, "WATCHDOG_PID": true,
	"WATCHDOG_USEC": true}

// Command is the call that runs prog with args in the placement: the call as it is with none, else systemd-run's,
// which runs prog in a scope of its own in the slice. prog stays the caller's child, with its stdin, its output and
// its exit code, and whatever it writes is the slice's. env is the env to run it with: nil for the caller's own.
func (p Placement) Command(prog string, args ...string) (string, []string, []string) {
	if p == "" {
		return prog, args, nil
	}
	argv := append([]string{"--user", "--scope", "--quiet", "--collect", "--slice=" + string(p), "--", prog}, args...)
	return "systemd-run", argv, managerEnv()
}

// Slot is a system's claim on the machine. Bind says its network exists; Release frees it.
type Slot interface {
	Bind()
	Release()
}

// Machine is what a caller takes as a value. Hold waits for a slot of the kind; Place never waits and takes
// no slot.
type Machine interface {
	Hold(kind, system string) (Slot, error)
	Place(kind, system string) (Placement, error)
	// Unplace stops the system's own slice, once its members are gone, so a removed system leaves no slice.
	Unplace(kind, system string) error
}

// noMachine is a machine crate places nothing on: a remote engine's, whose work runs on another machine. It still
// refuses a kind outside the four.
type noMachine struct{}

func (noMachine) Hold(kind, system string) (Slot, error) {
	if err := CheckKind(kind); err != nil {
		return nil, err
	}
	return noSlot{}, nil
}

func (noMachine) Place(kind, system string) (Placement, error) { return "", CheckKind(kind) }

func (noMachine) Unplace(kind, system string) error { return CheckKind(kind) }

type noSlot struct{}

func (noSlot) Bind()    {}
func (noSlot) Release() {}

type failing struct{ err error }

func (f failing) Hold(kind, system string) (Slot, error) {
	if err := CheckKind(kind); err != nil {
		return nil, err
	}
	return nil, f.err
}

// Unplace has nothing to stop: a failing machine placed nothing.
func (f failing) Unplace(kind, system string) error { return CheckKind(kind) }

func (f failing) Place(kind, system string) (Placement, error) {
	if err := CheckKind(kind); err != nil {
		return "", err
	}
	return "", f.err
}

// Default is the machine of this user and machine file, or of the defaults when there is no file. The machine's slice
// is CRATE_SLICE, or crate when the environment names none. A machine behind a remote engine places nothing and holds
// nothing: its work runs on the engine's machine.
func Default() Machine {
	if os.Getenv("CONTAINER_HOST") != "" {
		return noMachine{}
	}
	cfg, err := Read(Path())
	if err != nil {
		return failing{err: err}
	}
	if cfg == nil {
		cfg = Defaults()
	}
	return &ceiling{cfg: cfg, slice: sliceName()}
}

// sliceName is the machine's slice: CRATE_SLICE, or crate when the environment names none.
func sliceName() string {
	if slice := os.Getenv("CRATE_SLICE"); slice != "" {
		return slice
	}
	return "crate"
}

// Own is where crate's own lasting processes run, a queue worker or a postsubmit detached from what started it:
// crate's slice itself, under its line, so neither outlives nor dies with the unit or session that started it. ""
// behind a remote engine, whose machine is not this one.
func Own() Placement {
	if os.Getenv("CONTAINER_HOST") != "" {
		return ""
	}
	return Placement(sliceName() + ".slice")
}
