package system

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/matta-kelly/crate/internal/machine"
	"github.com/matta-kelly/crate/internal/member"
)

// engineRemote says podman is a client of a remote engine (podman-remote, or CONTAINER_HOST), asked once.
var engineRemote = sync.OnceValue(func() bool {
	if os.Getenv("CONTAINER_HOST") != "" {
		return true
	}
	out, err := exec.Command("podman", "info", "--format", "{{.Host.ServiceIsRemote}}").Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
})

func (s *System) isRemote() bool {
	if s.remote != nil {
		return s.remote()
	}
	return engineRemote()
}

// place is the system's slice on its machine, kept for the podman calls that write the store (call).
func (s *System) place() (machine.Placement, error) {
	placement, err := s.m.Place(s.spec.Kind, s.spec.Name)
	if err != nil {
		return "", err
	}
	s.placed = placement
	return placement, nil
}

// call runs a podman command, its output to the caller, and one error that names it. A call that writes the store
// runs in the system's slice (writes), so podman's own work, a pull or a layer's unpack and commit, is the slice's.
func (s *System) call(ctx context.Context, args ...string) (string, error) {
	if writes(args) {
		prog, argv, env := s.placed.Command("podman", args...)
		return s.run(ctx, prog, env, argv...)
	}
	return s.run(ctx, "podman", nil, args...)
}

// writes says a podman call writes the store or starts what does: a pull, a build, a container's run or create, and
// a kube play.
func writes(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "pull", "build", "run", "create":
		return true
	case "kube":
		return len(args) > 1 && args[1] == "play"
	}
	return false
}

// run runs one program, its output to the caller and its error naming the step.
func (s *System) run(ctx context.Context, prog string, env []string, args ...string) (string, error) {
	cmd := command(ctx, prog, env, args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	text := strings.TrimSpace(out.String())
	if text != "" {
		fmt.Fprintln(s.out(), text)
	}
	if ctx.Err() != nil {
		return text, fmt.Errorf("%s %s: ran past its limit: %w", prog, strings.Join(args, " "), ctx.Err())
	}
	if err != nil {
		return text, fmt.Errorf("%s %s: %s: %w", prog, strings.Join(args, " "), text, err)
	}
	return text, nil
}

func command(ctx context.Context, prog string, env []string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, prog, args...)
	if env != nil {
		cmd.Env = env
	}
	return cmd
}

// exists is a podman ask that exits 1 for no, and any other failure is an error.
func (s *System) exists(ctx context.Context, args ...string) (bool, error) {
	cmd := command(ctx, "podman", nil, args...)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	if ctx.Err() != nil {
		return false, fmt.Errorf("podman %s: ran past its limit: %w", strings.Join(args, " "), ctx.Err())
	}
	return false, fmt.Errorf("podman %s: %w", strings.Join(args, " "), err)
}

func (s *System) networkExists(ctx context.Context) (bool, error) {
	return s.exists(ctx, "network", "exists", s.spec.Name)
}

func (s *System) containerExists(ctx context.Context, name string) (bool, error) {
	return s.exists(ctx, "container", "exists", name)
}

func (s *System) podExists(ctx context.Context, name string) (bool, error) {
	return s.exists(ctx, "pod", "exists", name)
}

func (s *System) volumeExists(ctx context.Context, name string) (bool, error) {
	return s.exists(ctx, "volume", "exists", name)
}

func (s *System) running(ctx context.Context, name string) (bool, error) {
	cmd := command(ctx, "podman", nil, "inspect", name, "--format", "{{.State.Running}}")
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("podman inspect %s: %w", name, err)
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

// runArgs is one podman run of a member.
func (s *System) runArgs(spec member.Spec) []string {
	args := []string{"run", "-d", "--name", spec.Name}
	if spec.Once {
		args = []string{"run", "--rm", "--name", spec.Name}
	}
	if spec.Network != "" {
		args = append(args, "--network", spec.Network)
	}
	for _, alias := range spec.Aliases {
		args = append(args, "--network-alias", alias)
	}
	if spec.Userns != "" {
		args = append(args, "--userns="+spec.Userns)
	}
	if spec.User != "" {
		args = append(args, "--user", spec.User)
	}
	args = append(args, "--security-opt", "label=disable")
	if spec.ReadOnlyRoot {
		args = append(args, "--read-only")
	}
	for _, capability := range spec.DropCapabilities {
		args = append(args, "--cap-drop", capability)
	}
	if spec.NoNewPrivileges {
		args = append(args, "--security-opt", "no-new-privileges")
	}
	if spec.Health != "" {
		args = append(args, "--health-cmd", spec.Health, "--health-interval", "disable")
	}
	if spec.CPUs != "" {
		args = append(args, "--cpus", spec.CPUs)
	}
	if spec.Memory != "" {
		args = append(args, "--memory", spec.Memory)
	}
	if spec.Placement != "" {
		args = append(args, "--cgroup-parent", string(spec.Placement))
	} else if !s.isRemote() {
		// --cgroup-manager is the local engine's own flag: podman-remote refuses it, and the engine it reaches
		// keeps its own manager.
		args = append(args, "--cgroup-manager=cgroupfs")
	}
	for _, mount := range spec.Mounts {
		volume := mount.Source + ":" + mount.Target
		if mount.ReadOnly {
			volume += ":ro"
		}
		args = append(args, "-v", volume)
	}
	for _, volume := range spec.Volumes {
		mode := ":U"
		if volume.Overlay {
			mode = ":O"
		}
		args = append(args, "-v", volume.Name+":"+volume.Path+mode)
	}
	for _, env := range spec.Env {
		args = append(args, "-e", env)
	}
	args = append(args, spec.Image)
	return append(args, spec.Command...)
}

// playOne writes one service's document and plays it alone on the system's network.
func (s *System) playOne(ctx context.Context, service Service, placement machine.Placement) error {
	document, err := os.CreateTemp("", "crate-services-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(document.Name())
	if _, err := document.WriteString(service.Document); err != nil {
		document.Close()
		return err
	}
	if err := document.Close(); err != nil {
		return err
	}
	args := []string{"kube", "play", "--network", s.spec.Name + ":alias=" + service.Alias, document.Name()}
	if placement == "" {
		_, err := s.call(ctx, args...)
		return err
	}
	// The pods' containers stay in the scope, in the slice: podman makes no cgroup of its own for them.
	conf, err := os.CreateTemp("", "crate-containers-*.conf")
	if err != nil {
		return err
	}
	defer os.Remove(conf.Name())
	if _, err := conf.WriteString("[containers]\ncgroups = \"disabled\"\n"); err != nil {
		conf.Close()
		return err
	}
	if err := conf.Close(); err != nil {
		return err
	}
	prog, argv, env := placement.Command("podman", args...)
	_, err = s.run(ctx, prog, append(env, "CONTAINERS_CONF_OVERRIDE="+conf.Name()), argv...)
	return err
}

// terminal says f is a terminal, so a command run in a member gets one.
func terminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
