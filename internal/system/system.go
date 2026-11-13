// Package system makes a workspace as a running system from its checkout's declaration: its network, its
// services as a pod each, its tools member, its setup, its app and its routes, and its removal.
package system

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/matta-kelly/crate/internal/declaration"
	"github.com/matta-kelly/crate/internal/devimage"
	"github.com/matta-kelly/crate/internal/limit"
	"github.com/matta-kelly/crate/internal/machine"
	"github.com/matta-kelly/crate/internal/member"
)

// The four kinds of a system, as the machine names them.
const (
	Work       = machine.Work
	Attempt    = machine.Attempt
	Check      = machine.Check
	Postsubmit = machine.Postsubmit
)

// Mount is a host folder the caller adds, read-only, at the same path inside.
type Mount struct {
	Path string
}

// Spec is one system. Kind is one of the four; Name is the gate's name or a workspace's; Checkout is the
// folder that holds crate.toml. Keep names the volumes a gate's Up leaves in place when it clears a crash's
// system, and Out is where Up's and Remove's steps and failures go.
type Spec struct {
	Kind     string
	Name     string
	Checkout string
	Mounts   []Mount
	Env      []string
	Keep     []string
	Out      io.Writer
	Router   Router // a person's workspace's routes; nil for none
	Image    string // the tools member's image when the caller has one; "" for the repo's dev image
}

// System is a system of one name on one machine.
type System struct {
	m      machine.Machine
	spec   Spec
	slot   machine.Slot
	bound  bool
	image  string
	played []Service
	remote func() bool // whether podman is a client of a remote engine; engineRemote when nil
}

// New makes a system on m. It starts nothing.
func New(m machine.Machine, spec Spec) *System { return &System{m: m, spec: spec} }

// Made says whether the system's network exists, so the caller made it at least once.
func (s *System) Made() (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), limit.Podman())
	defer cancel()
	return s.networkExists(ctx)
}

// Hold waits for the system's slot on its machine, bounded by limit.Hold.
func (s *System) Hold() error {
	if err := CheckName(s.spec.Name); err != nil {
		return err
	}
	type held struct {
		slot machine.Slot
		err  error
	}
	waiting := make(chan held, 1)
	go func() {
		slot, err := s.m.Hold(s.spec.Kind, s.spec.Name)
		waiting <- held{slot: slot, err: err}
	}()
	select {
	case h := <-waiting:
		if h.err != nil {
			return h.err
		}
		if h.slot == nil {
			return fmt.Errorf("hold: the machine gave no slot for %s", s.spec.Name)
		}
		s.slot = h.slot
		return nil
	case <-time.After(limit.Hold()):
		go func() {
			if h := <-waiting; h.slot != nil {
				h.slot.Release()
			}
		}()
		return fmt.Errorf("hold: waited past the limit of %s for a slot for %s", limit.Hold(), s.spec.Name)
	}
}

// Image puts the system's dev image on the machine, placed through its machine's Place. A Spec that names an image
// takes that one.
func (s *System) Image() error {
	if s.spec.Image != "" {
		s.image = s.spec.Image
		return nil
	}
	placement, err := s.m.Place(s.spec.Kind, s.spec.Name)
	if err != nil {
		return err
	}
	tag, err := devimage.Local(devimage.Options{Root: s.spec.Checkout, Stderr: s.out()}, placement)
	if err != nil {
		return err
	}
	s.image = tag
	return nil
}

// Up makes the network, the services, the tools member, the setup and the app, all by ctx.
func (s *System) Up(ctx context.Context) (err error) {
	decl, services, err := s.read()
	if err != nil {
		return err
	}
	folders, err := decl.Tools.Folders()
	if err != nil {
		return err
	}
	if s.spec.Kind == Attempt {
		made, err := s.Made()
		if err != nil {
			return err
		}
		if made {
			if s.slot != nil {
				s.slot.Release()
				s.slot = nil
			}
			return fmt.Errorf("this checkout's workspace %s is up: an attempt runs in a worktree of its own, so run crate down first", s.spec.Name)
		}
	}
	if s.spec.Kind == Check || s.spec.Kind == Postsubmit {
		if err := s.remove(s.spec.Keep, false); err != nil {
			return err
		}
	}
	defer func() {
		if err != nil {
			_ = s.remove(s.spec.Keep, true)
		}
	}()

	made, err := s.networkExists(ctx)
	if err != nil {
		return err
	}
	if !made {
		args := []string{"network", "create", "--opt", "isolate=strict"}
		if s.spec.Kind == Attempt {
			args = append(args, "--internal")
		}
		args = append(args, s.spec.Name)
		if _, err := s.call(ctx, args...); err != nil {
			return err
		}
	}
	if s.slot != nil && !s.bound {
		s.slot.Bind()
		s.bound = true
	}

	if err := s.play(ctx, services, false); err != nil {
		return err
	}
	created, err := s.tools(ctx, decl, folders)
	if err != nil {
		return err
	}
	if created && decl.Tools.Setup != "" {
		if err := s.setup(ctx, decl.Tools.Setup); err != nil {
			return err
		}
	}
	return s.app(ctx, decl, folders)
}

// Run runs argv in the tools member.
func (s *System) Run(ctx context.Context, argv []string, out io.Writer) (int, error) {
	dir := s.spec.Checkout
	if wd, err := os.Getwd(); err == nil && within(s.spec.Checkout, wd) {
		dir = wd
	}
	args := []string{"exec", "--workdir", dir}
	if terminal(os.Stdin) {
		args = append(args, "-it")
	}
	args = append(args, s.spec.Name+"-tools")
	args = append(args, argv...)
	cmd := command(ctx, "podman", nil, args...)
	cmd.Stdout, cmd.Stderr = out, out
	if terminal(os.Stdin) {
		cmd.Stdin = os.Stdin
	}
	err := cmd.Run()
	if ctx.Err() != nil {
		return 1, fmt.Errorf("run: stopped when its context ended: %w", ctx.Err())
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	return 0, err
}

// Services makes the services again, with the tools member left running.
func (s *System) Services() error {
	_, services, err := s.read()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit.Up())
	defer cancel()
	return s.play(ctx, services, true)
}

// Logs writes a member's output to out.
func (s *System) Logs(member string, out io.Writer) error {
	name := s.spec.Name + "-tools"
	switch {
	case member == "app":
		name = s.appName()
	case member != "" && member != "tools":
		name = s.spec.Name + "-" + member + "-" + member
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit.Podman())
	defer cancel()
	cmd := command(ctx, "podman", nil, "logs", name)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("logs %s: %w", member, err)
	}
	return nil
}

// Remove removes the system, all but the named volumes, and releases its slot.
func (s *System) Remove(keep []string) error { return s.remove(keep, true) }

// remove takes the system apart. It releases the slot only when it finishes, and writes a line only for a
// part it found and removed, so a second down says nothing.
func (s *System) remove(keep []string, release bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), limit.Up())
	defer cancel()
	kept := map[string]bool{}
	for _, name := range keep {
		kept[name] = true
	}

	decl, services, err := s.read()
	if err != nil {
		services = s.played
	}
	var tools []string
	if err == nil {
		if folders, ferr := decl.Tools.Folders(); ferr == nil {
			for _, folder := range folders {
				tools = append(tools, member.VolumeName(s.spec.Name, folder))
			}
		}
	}
	var serviceVolumes []string
	for _, service := range services {
		serviceVolumes = append(serviceVolumes, volumesOf(service.Document)...)
	}

	if err := s.removeApp(ctx); err != nil {
		return err
	}
	for _, service := range services {
		made, err := s.podExists(ctx, service.Name)
		if err != nil {
			return err
		}
		if made {
			if _, err := s.call(ctx, "pod", "rm", "-f", service.Name); err != nil {
				return fmt.Errorf("down: the pod %s is still there: %w", service.Name, err)
			}
		}
	}

	container := s.spec.Name + "-tools"
	made, err := s.containerExists(ctx, container)
	if err != nil {
		return err
	}
	if made {
		if _, err := s.call(ctx, "rm", "-f", "-t", "0", container); err != nil {
			return fmt.Errorf("down: the container %s is still there: %w", container, err)
		}
	}

	for _, volume := range append(tools, serviceVolumes...) {
		if kept[volume] {
			continue
		}
		made, err := s.volumeExists(ctx, volume)
		if err != nil {
			return err
		}
		if made {
			if _, err := s.call(ctx, "volume", "rm", volume); err != nil {
				return fmt.Errorf("down: the volume %s is still there: %w", volume, err)
			}
		}
	}

	made, err = s.networkExists(ctx)
	if err != nil {
		return err
	}
	if made {
		if _, err := s.call(ctx, "network", "rm", s.spec.Name); err != nil {
			return fmt.Errorf("down: the network %s is still there: %w", s.spec.Name, err)
		}
	}

	if release && s.slot != nil {
		s.slot.Release()
		s.slot = nil
		s.bound = false
	}
	return nil
}

// play makes each service a pod of its own, unless it is there and remake is false.
func (s *System) play(ctx context.Context, services []Service, remake bool) error {
	for _, service := range services {
		made, err := s.podExists(ctx, service.Name)
		if err != nil {
			return err
		}
		if made {
			if !remake {
				continue
			}
			if _, err := s.call(ctx, "pod", "rm", "-f", service.Name); err != nil {
				return err
			}
		}
		placement, err := s.m.Place(s.spec.Kind, s.spec.Name)
		if err != nil {
			return err
		}
		if err := s.playOne(ctx, service, placement); err != nil {
			return err
		}
	}
	s.played = services
	return nil
}

// tools makes or starts the tools member and says whether it is new, so setup runs once.
func (s *System) tools(ctx context.Context, decl declaration.Declaration, folders []string) (bool, error) {
	spec := member.Tools(member.ToolsOptions{
		System:  s.spec.Name,
		Root:    s.spec.Checkout,
		Image:   s.image,
		Folders: folders,
		Env:     decl.Tools.Env,
	})
	for _, mount := range s.spec.Mounts {
		spec.Mounts = append(spec.Mounts, member.Mount{Source: mount.Path, Target: mount.Path, ReadOnly: true})
	}
	spec.Env = append(spec.Env, s.spec.Env...)
	if url := s.appURL(decl.App); url != "" {
		spec.Env = append(spec.Env, "CRATE_APP_URL="+url)
	}

	for _, volume := range spec.Volumes {
		made, err := s.volumeExists(ctx, volume.Name)
		if err != nil {
			return false, err
		}
		if !made {
			if _, err := s.call(ctx, "volume", "create", volume.Name); err != nil {
				return false, err
			}
		}
	}

	made, err := s.containerExists(ctx, spec.Name)
	if err != nil {
		return false, err
	}
	if made {
		running, err := s.running(ctx, spec.Name)
		if err != nil {
			return false, err
		}
		if running {
			return false, nil
		}
		placement, err := s.m.Place(s.spec.Kind, s.spec.Name)
		if err != nil {
			return false, err
		}
		spec.Placement = placement
		if _, err := s.call(ctx, "start", spec.Name); err != nil {
			return false, err
		}
		return false, nil
	}

	placement, err := s.m.Place(s.spec.Kind, s.spec.Name)
	if err != nil {
		return false, err
	}
	spec.Placement = placement
	if _, err := s.call(ctx, s.runArgs(spec)...); err != nil {
		return false, err
	}
	return true, nil
}

// setup runs the [tools] setup as node, in the checkout's root, its output to the caller.
func (s *System) setup(ctx context.Context, command string) error {
	out, err := s.call(ctx, "exec", "--workdir", s.spec.Checkout, s.spec.Name+"-tools", "sh", "-c", command)
	if ctx.Err() != nil {
		return fmt.Errorf("up: ran past its limit at setup: %w", ctx.Err())
	}
	if err != nil {
		return fmt.Errorf("setup: %s: %w", strings.TrimSpace(out), err)
	}
	return nil
}

// read is the checkout's declaration and its rendered services, refused before anything starts.
func (s *System) read() (declaration.Declaration, []Service, error) {
	data, err := os.ReadFile(filepath.Join(s.spec.Checkout, "crate.toml"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return declaration.Declaration{}, nil, err
	}
	decl, err := declaration.Parse(data)
	if err != nil {
		return declaration.Declaration{}, nil, err
	}
	if decl.Services == "" {
		return decl, nil, nil
	}
	services, err := os.ReadFile(filepath.Join(s.spec.Checkout, decl.Services))
	if err != nil {
		return declaration.Declaration{}, nil, fmt.Errorf("services file %s: %w", decl.Services, err)
	}
	rendered, err := ServicesOf(services, s.spec.Name)
	if err != nil {
		return declaration.Declaration{}, nil, err
	}
	return decl, rendered, nil
}

// out is where the system's steps and failures go.
func (s *System) out() io.Writer {
	if s.spec.Out == nil {
		return io.Discard
	}
	return s.spec.Out
}

// within says dir is root or below it.
func within(root, dir string) bool {
	rel, err := filepath.Rel(root, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
