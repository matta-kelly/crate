// Package machine is the seam between crate and the machine it runs on: the slice each kind of crate's
// work goes in, and the file that says how many of a kind may run. Here the default machine orders the work
// in the user's systemd and keeps the slots.
package machine

import (
	"fmt"
	"os"
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
}

// noMachine is a machine crate places nothing on: a remote engine's, or one with no machine file. It still
// refuses a kind outside the four.
type noMachine struct{}

func (noMachine) Hold(kind, system string) (Slot, error) {
	if err := CheckKind(kind); err != nil {
		return nil, err
	}
	return noSlot{}, nil
}

func (noMachine) Place(kind, system string) (Placement, error) { return "", CheckKind(kind) }

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

func (f failing) Place(kind, system string) (Placement, error) {
	if err := CheckKind(kind); err != nil {
		return "", err
	}
	return "", f.err
}

// Default is the machine of this user and machine file. The machine's slice is CRATE_SLICE, or crate when the
// environment names none. A machine with no file or behind a remote engine places nothing and holds nothing.
func Default() Machine {
	if os.Getenv("CONTAINER_HOST") != "" {
		return noMachine{}
	}
	cfg, err := Read(Path())
	if err != nil {
		return failing{err: err}
	}
	if cfg == nil {
		return noMachine{}
	}
	slice := os.Getenv("CRATE_SLICE")
	if slice == "" {
		slice = "crate"
	}
	return &ceiling{cfg: cfg, slice: slice}
}
