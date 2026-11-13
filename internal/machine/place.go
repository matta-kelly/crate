package machine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/matta-kelly/crate/internal/limit"
)

// ceiling is the machine file's order, placed in the user's systemd under the slice CRATE_SLICE names.
type ceiling struct {
	cfg   *Config
	slice string
}

func (m *ceiling) Place(kind, system string) (Placement, error) {
	if err := CheckKind(kind); err != nil {
		return "", err
	}
	leaf := m.leaf(kind, system)
	if err := startSlice(leaf); err != nil {
		return "", err
	}
	kindSlice := m.slice + "-" + kind + ".slice"
	if err := systemd("systemctl --user set-property", "systemctl", "--user", "set-property", "--runtime",
		kindSlice, "CPUWeight="+strconv.Itoa(cpuWeight(kind))); err != nil {
		return "", fmt.Errorf("the %s kind's slice %s: CPUWeight: %w", kind, kindSlice, err)
	}
	return Placement(leaf), nil
}

// Unplace stops the system's leaf slice. A leaf that is not there is already gone.
func (m *ceiling) Unplace(kind, system string) error {
	if err := CheckKind(kind); err != nil {
		return err
	}
	leaf := m.leaf(kind, system)
	err := systemd("systemctl --user stop", "systemctl", "--user", "stop", leaf)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "not loaded") {
		return nil
	}
	return err
}

// leaf is the slice a system's members go in. The system's name is escaped as systemd-escape does, since a - in
// a slice name is a level.
func (m *ceiling) leaf(kind, system string) string {
	return m.slice + "-" + kind + "-" + escapeSystem(system) + ".slice"
}

// cpuWeight orders the kinds: a person's work first, a check next, postsubmit and attempts last.
func cpuWeight(kind string) int {
	switch kind {
	case Work:
		return 400
	case Check:
		return 200
	default:
		return 100
	}
}

func escapeSystem(system string) string {
	var b strings.Builder
	for i := 0; i < len(system); i++ {
		c := system[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == ':' {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, `\x%02x`, c)
	}
	return b.String()
}

// startSlice makes the slice and its parents. systemctl can start a slice with no unit file; an older manager
// that cannot is given a short scope in the slice instead.
func startSlice(leaf string) error {
	err := systemd("systemctl --user start", "systemctl", "--user", "start", leaf)
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "not found") || strings.Contains(msg, "not loaded") {
		return systemd("systemd-run --user", "systemd-run", "--user", "--scope", "--slice="+leaf, "true")
	}
	return err
}

// systemd runs one systemd call with its limit.
func systemd(name string, args ...string) error {
	_, err := runTool(name, limit.Systemd(), managerEnv(), args...)
	return err
}

// toolError is one failed call: the name it goes by, the tool's message and its exit code. The code lets a
// caller tell a plain no from a failure; the name and the message are the whole of the error's text.
type toolError struct {
	name    string
	msg     string
	code    int
	timeout time.Duration
}

func (e *toolError) Error() string {
	if e.timeout > 0 {
		return fmt.Sprintf("%s did not finish within %v", e.name, e.timeout)
	}
	return fmt.Sprintf("%s: %s", e.name, e.msg)
}

// runTool is the one home of every systemctl, systemd-run and podman call. It ends the call's whole process
// group at its limit, and its error names the call and holds the tool's message.
func runTool(name string, within time.Duration, env []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", &toolError{name: name, timeout: within}
	}
	if err != nil {
		msg := strings.TrimSpace(out.String())
		if msg == "" {
			msg = err.Error()
		}
		code := -1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		}
		return "", &toolError{name: name, msg: msg, code: code}
	}
	return out.String(), nil
}

// managerEnv is the process's environment with the user's runtime folder and bus named when a hook left them out.
func managerEnv() []string {
	env := os.Environ()
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if runtime != "" {
		return env
	}
	runtime = "/run/user/" + strconv.Itoa(os.Getuid())
	env = append(env, "XDG_RUNTIME_DIR="+runtime)
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		env = append(env, "DBUS_SESSION_BUS_ADDRESS=unix:path="+runtime+"/bus")
	}
	return env
}
