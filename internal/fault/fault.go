// Package fault says whose fault a failure is. A failure of the change itself (its check, its setup, its build) is
// marked with Change; any other failure is crate's machine's (podman, a pull, the network, a slot, a tree, the
// cache), and is an error. Only the change's own failures are marked, so a failure nobody marked never blames a
// change.
package fault

import (
	"errors"

	"github.com/matta-kelly/crate/internal/process"
)

type change struct{ err error }

func (c change) Error() string { return c.err.Error() }
func (c change) Unwrap() error { return c.err }

// Change marks err as the change's own failure. nil stays nil.
func Change(err error) error {
	if err == nil || IsChange(err) {
		return err
	}
	return change{err}
}

// IsChange says err is, or wraps, a failure marked as the change's own.
func IsChange(err error) bool {
	var c change
	return errors.As(err, &c)
}

// Own marks err as the change's own when the program exited by itself with a code of the command it ran: podman
// gives a container's exit code, and a build step's, as its own, and keeps 125 to 127 for its own failures. Any other
// failure (podman's, a limit, a stop) stays as it is.
func Own(err error) error {
	if code := process.ExitCode(err); (code >= 1 && code <= 124) || code >= 128 {
		return Change(err)
	}
	return err
}
