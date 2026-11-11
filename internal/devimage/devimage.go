// Package devimage finds, builds and pushes a repo's dev image. A stub: da-hammer builds it.
package devimage

import (
	"io"

	"github.com/matta-kelly/crate/internal/machine"
)

// Options says which checkout and where the output of a build goes.
type Options struct {
	Root   string    // the checkout
	Name   string    // the --name flag, or ""
	Stderr io.Writer // a build's and a pull's output
}

// Local puts <image>:<hash> in this machine's store and returns the tag. The build runs in placement.
func Local(o Options, placement machine.Placement) (string, error) { return "", nil }

// Ensure builds, smokes and pushes <image>:<hash> only when the registry lacks it, and returns its ref.
func Ensure(o Options, placement machine.Placement) (string, error) { return "", nil }
