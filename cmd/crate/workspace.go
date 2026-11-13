package main

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/matta-kelly/crate/internal/declaration"
	"github.com/matta-kelly/crate/internal/devimage"
	"github.com/matta-kelly/crate/internal/machine"
	"github.com/matta-kelly/crate/internal/record"
	"github.com/matta-kelly/crate/internal/router"
	"github.com/matta-kelly/crate/internal/system"
)

// workspace is one checkout's running copy of its repo: its folder, its repo's name, and its own.
type workspace struct {
	root string
	repo string
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
	return &workspace{root: root, repo: declared, name: name}, nil
}

// head is the commit the checkout is at; "" when it has none.
func (w *workspace) head() string {
	out, err := exec.Command("git", "-C", w.root, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// system is this workspace as a system of one kind, on the default machine, its steps on rec (nil for none). A
// person's workspace gets the machine's router for its app's routes.
func (w *workspace) system(kind string, out io.Writer, rec *record.Run) *system.System {
	spec := system.Spec{Kind: kind, Name: w.name, Checkout: w.root, Out: out, Record: rec}
	if kind == system.Work {
		spec.Router = router.New(machine.Default())
	}
	return system.New(machine.Default(), spec)
}
