package system

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/matta-kelly/crate/internal/declaration"
	"github.com/matta-kelly/crate/internal/member"
)

// A check and postsubmit run [tools] setup once per key, not once per run. The slot's kept volumes are the base: setup
// fills them once, from a read-only checkout, and each run mounts them as overlays, so it reads what setup made and
// what it writes is gone with it. Every run starts from the same base, as if setup had just run.
//
// The key is what setup's output depends on: the image (a dev image's tag is the hash of [dev] inputs, which name the
// lockfiles), the setup command, the volumes and [tools] env. It is kept as a label on each base volume.
//
// A setup that writes outside its volumes (a package's prepare that builds into the checkout) cannot fill a base: the
// read-only checkout refuses it. Its key is labelled run, and each run then runs setup in the tools member, as a
// person's workspace does.

// setupLabel is the label of a base volume: the key its setup ran for, with " run" when that setup runs in each run.
const setupLabel = "crate.setup"

// How a run's volumes were readied for its setup, as the record notes it.
const (
	kept    = "kept"    // the bases hold this key's setup: no setup
	primed  = "primed"  // setup filled the bases now
	refused = "refused" // the prime found that this key's setup writes outside its volumes: it runs in the tools member
	run     = "run"     // the label says so already: setup runs in the tools member
)

// gate says this kind is a check or postsubmit, whose kept volumes are bases.
func (s *System) gate() bool { return s.spec.Kind == Check || s.spec.Kind == Postsubmit }

// setupKey is the key of setup's output, as the label holds it.
func (s *System) setupKey(decl declaration.Declaration, folders []string) string {
	h := sha256.New()
	fmt.Fprintf(h, "image %s\nsetup %s\nvolumes %s\n", s.image, decl.Tools.Setup, strings.Join(folders, " "))
	keys := make([]string, 0, len(decl.Tools.Env))
	for key := range decl.Tools.Env {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		fmt.Fprintf(h, "env %s=%s\n", key, decl.Tools.Env[key])
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

// warm readies the bases for this run's setup: kept when each holds the key, run when the key's setup runs in the tools
// member, else primed, or refused when the prime finds that setup writes outside its volumes.
func (s *System) warm(ctx context.Context, decl declaration.Declaration, folders []string) (string, error) {
	key := s.setupKey(decl, folders)
	labels, err := s.setupLabels(ctx, folders)
	if err != nil {
		return "", err
	}
	switch {
	case all(labels, key):
		return kept, nil
	case all(labels, key+" "+run):
		return run, nil
	}
	if err := s.bases(ctx, folders, key); err != nil {
		return "", err
	}
	err = s.prime(ctx, decl, folders)
	if ctx.Err() != nil {
		return "", fmt.Errorf("up: ran past its limit at setup: %w", ctx.Err())
	}
	if err == nil {
		return primed, nil
	}
	fmt.Fprintf(s.out(), "crate: setup did not fill its volumes from a read-only checkout, so it runs in each %s: %v\n", s.spec.Kind, err)
	return refused, s.bases(ctx, folders, key+" "+run)
}

// setupLabels is each base's label, "" for a base that is not there.
func (s *System) setupLabels(ctx context.Context, folders []string) ([]string, error) {
	labels := make([]string, len(folders))
	for i, folder := range folders {
		name := member.VolumeName(s.spec.Name, folder)
		made, err := s.volumeExists(ctx, name)
		if err != nil || !made {
			return labels, err
		}
		label, err := s.call(ctx, "volume", "inspect", name, "--format", `{{index .Labels "`+setupLabel+`"}}`)
		if err != nil {
			return labels, err
		}
		labels[i] = label
	}
	return labels, nil
}

func all(labels []string, want string) bool {
	return !slices.ContainsFunc(labels, func(label string) bool { return label != want })
}

// bases makes each base volume new and empty, with the label.
func (s *System) bases(ctx context.Context, folders []string, label string) error {
	for _, folder := range folders {
		name := member.VolumeName(s.spec.Name, folder)
		if _, err := s.call(ctx, "volume", "rm", "-f", name); err != nil {
			return err
		}
		if _, err := s.call(ctx, "volume", "create", "--label", setupLabel+"="+label, name); err != nil {
			return err
		}
	}
	return nil
}

// prime runs setup once, in a member like the tools member, with the checkout read-only and the bases its own, so what
// setup makes is in the bases and nowhere else. Each volume's folder is made in the checkout first: a read-only
// checkout cannot take a mount point.
func (s *System) prime(ctx context.Context, decl declaration.Declaration, folders []string) error {
	for _, folder := range folders {
		if err := os.MkdirAll(filepath.Join(s.spec.Checkout, folder), 0o755); err != nil {
			return err
		}
	}
	spec := s.toolsSpec(decl, folders)
	spec.Name = s.spec.Name + "-prime"
	spec.Aliases = nil
	spec.Once = true
	spec.Command = []string{"sh", "-c", "cd " + shellQuote(s.spec.Checkout) + " && " + decl.Tools.Setup}
	for i := range spec.Mounts {
		if spec.Mounts[i].Source == s.spec.Checkout {
			spec.Mounts[i].ReadOnly = true
		}
	}
	placement, err := s.m.Place(s.spec.Kind, s.spec.Name)
	if err != nil {
		return err
	}
	spec.Placement = placement
	_, err = s.call(ctx, s.runArgs(spec)...)
	return err
}
