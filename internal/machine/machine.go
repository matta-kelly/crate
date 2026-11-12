// Package machine is the seam between crate and the machine it runs on: what a system may take of it, and
// the file that says what the machine has. Here the default machine places nothing and holds nothing.
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

// The names crate itself uses for a system. A workspace's name is <name>-<folder>, so a name with no "-"
// never clashes with one.
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

type noCeiling struct{}

func (noCeiling) Hold(kind, system string) (Slot, error) {
	if err := CheckKind(kind); err != nil {
		return nil, err
	}
	return noSlot{}, nil
}

func (noCeiling) Place(kind, system string) (Placement, error) { return "", CheckKind(kind) }

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
// environment names none. A machine with no file or behind a remote engine has no ceiling: it places nothing
// and holds nothing.
func Default() Machine {
	if os.Getenv("CONTAINER_HOST") != "" {
		return noCeiling{}
	}
	path := Path()
	cfg, err := Read(path)
	if err != nil {
		return failing{err: err}
	}
	if cfg == nil {
		return noCeiling{}
	}
	slice := os.Getenv("CRATE_SLICE")
	if slice == "" {
		slice = "crate"
	}
	return &ceiling{cfg: cfg, path: path, slice: slice}
}
