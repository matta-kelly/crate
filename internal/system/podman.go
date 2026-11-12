package system

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/matta-kelly/crate/internal/machine"
	"github.com/matta-kelly/crate/internal/member"
)

// call runs a podman command, its output to the caller, and one error that names it.
func (s *System) call(ctx context.Context, args ...string) (string, error) {
	return s.run(ctx, "podman", nil, args...)
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
	if spec.CPUs != "" {
		args = append(args, "--cpus", spec.CPUs)
	}
	if spec.Memory != "" {
		args = append(args, "--memory", spec.Memory)
	}
	if spec.Placement != "" {
		args = append(args, "--cgroup-parent", string(spec.Placement))
	} else {
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
		args = append(args, "-v", volume.Name+":"+volume.Path+":U")
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
	env := append(os.Environ(), "CONTAINERS_CONF_OVERRIDE="+conf.Name())
	args = append([]string{"--user", "--scope", "--slice=" + string(placement), "podman"}, args...)
	_, err = s.run(ctx, "systemd-run", env, args...)
	return err
}

// terminal says f is a terminal, so a command run in a member gets one.
func terminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
