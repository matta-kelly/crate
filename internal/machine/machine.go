// Package machine is the seam between crate and the machine it runs on. A stub: da-hammer builds it.
package machine

// The four kinds of a claim on the machine.
const (
	Work       = "work"
	Check      = "check"
	Postsubmit = "postsubmit"
	Attempt    = "attempt"
)

// Placement is a slice name, or "" for none.
type Placement string

// Slot is a system's claim on the machine.
type Slot interface {
	Bind()
	Release()
}

// Machine is what a caller takes as a value.
type Machine interface {
	Hold(kind, system string) (Slot, error)
	Place(kind, system string) (Placement, error)
}

type stub struct{}

func (stub) Hold(kind, system string) (Slot, error)       { return nil, nil }
func (stub) Place(kind, system string) (Placement, error) { return "", nil }

// Default is the machine of this user and machine file.
func Default() Machine { return stub{} }

// Count is the [count] table of the machine file.
type Count struct {
	Check      int
	Postsubmit int
	Attempt    int
}

// Config is the machine file.
type Config struct {
	CPUs   string
	Memory string
	Count  Count
}

// Read reads the machine file at path. A path with no file gives nil and no error.
func Read(path string) (*Config, error) { return nil, nil }
