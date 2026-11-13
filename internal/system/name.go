package system

import (
	"fmt"

	"github.com/matta-kelly/crate/internal/machine"
)

// Name is a workspace's name: <declared>-<folder>, so a second checkout in another folder is a workspace of its own.
// A checkout in a folder of the declared name is the name alone, as is one with no name declared, but for a name
// crate keeps for itself (router, build).
func Name(declared, folder string) string {
	if declared == "" {
		declared = folder
	}
	if declared == folder && folder != machine.Router && folder != machine.Build {
		return folder
	}
	return declared + "-" + folder
}

// CheckName refuses a name that is not a DNS label, and says why.
func CheckName(name string) error {
	if len(name) == 0 || len(name) > 63 {
		return fmt.Errorf("the name %q is not a DNS label: expected 1 to 63 characters", name)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		letter := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
		if !letter && c != '-' {
			return fmt.Errorf("the name %q is not a DNS label: expected a-z, 0-9 and -", name)
		}
		if c == '-' && (i == 0 || i == len(name)-1) {
			return fmt.Errorf("the name %q is not a DNS label: expected no - at either end", name)
		}
	}
	return nil
}
