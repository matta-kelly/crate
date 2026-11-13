package main

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/matta-kelly/crate/internal/declaration"
	"github.com/matta-kelly/crate/internal/devimage"
	"github.com/matta-kelly/crate/internal/machine"
	"github.com/matta-kelly/crate/internal/router"
	"github.com/matta-kelly/crate/internal/system"
)

// workspace is one checkout's running copy of its repo: its folder, its declaration, and its name.
type workspace struct {
	root string
	name string
}

// openWorkspace reads the checkout that holds the caller's folder and settles its name.
func openWorkspace() (*workspace, error) {
	root, err := devimage.Toplevel(".")
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(root, "crate.toml"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	var decl declaration.Declaration
	if err == nil {
		if decl, err = declaration.Parse(data); err != nil {
			return nil, err
		}
	}
	declared := decl.Name
	if declared == "" {
		if declared, err = devimage.RepoName(root); err != nil {
			return nil, err
		}
	}
	name := system.Name(declared, filepath.Base(root))
	if err := system.CheckName(name); err != nil {
		return nil, err
	}
	return &workspace{root: root, name: name}, nil
}

// system is this workspace as a system of one kind, on the default machine. A person's workspace gets the
// machine's router for its app's routes.
func (w *workspace) system(kind string, out io.Writer) *system.System {
	spec := system.Spec{Kind: kind, Name: w.name, Checkout: w.root, Out: out}
	if kind == system.Work {
		spec.Router = router.New(machine.Default())
	}
	return system.New(machine.Default(), spec)
}
