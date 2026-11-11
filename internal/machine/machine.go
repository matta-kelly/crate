// Package machine is the seam between crate and the machine it runs on: what a system may take of it, and
// the file that says what the machine has. Here the default machine places nothing and holds nothing.
package machine

import "fmt"

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

// Default is the machine of this user and machine file.
func Default() Machine { return noCeiling{} }
