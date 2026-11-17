package machine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/matta-kelly/crate/internal/limit"
)

// ceiling is the machine's bound and order, placed in the user's systemd under the slice CRATE_SLICE names.
type ceiling struct {
	cfg    *Config
	slice  string
	shaped sync.Once
	shape  error
}

// The weights of crate's own slice: below a sibling's default of 100, so the desktop, or a node's other services, go
// first when both want the CPU or the disk. A weight costs nothing on an idle machine.
const (
	sliceCPUWeight = 50
	sliceIOWeight  = 50
)

// controllers are the ones crate.slice must have: cpu for its weight, memory for its line, and io, without which the
// kernel does not keep its writers' unwritten data to the line.
var controllers = []string{"cpu", "io", "memory"}

func (m *ceiling) Place(kind, system string) (Placement, error) {
	if err := CheckKind(kind); err != nil {
		return "", err
	}
	leaf := m.leaf(kind, system)
	if err := startSlice(leaf); err != nil {
		return "", err
	}
	m.shaped.Do(func() { m.shape = m.bound() })
	if m.shape != nil {
		return "", m.shape
	}
	kindSlice := m.slice + "-" + kind + ".slice"
	if err := systemd("systemctl --user set-property", "systemctl", "--user", "set-property", "--runtime",
		kindSlice, "CPUWeight="+strconv.Itoa(cpuWeight(kind))); err != nil {
		return "", fmt.Errorf("the %s kind's slice %s: CPUWeight: %w", kind, kindSlice, err)
	}
	return Placement(leaf), nil
}

// bound draws crate's line under all its work: crate.slice's MemoryHigh, the machine file's line of the machine's
// memory, and its weights. MemoryHigh slows crate's own work past the line and kills nothing, and it holds crate's
// unwritten data to a share of it, so a flush of crate's writes stays small. The properties are --runtime: they live
// with the user's manager, and the next Place sets them again after it restarts. A slice that lacks a controller is
// an error that names it, so crate never runs unbounded without saying so.
func (m *ceiling) bound() error {
	total, err := memTotal()
	if err != nil {
		return err
	}
	unit := m.slice + ".slice"
	if err := systemd("systemctl --user set-property", "systemctl", "--user", "set-property", "--runtime", unit,
		"MemoryHigh="+strconv.FormatUint(m.cfg.Line.Of(total), 10),
		"CPUWeight="+strconv.Itoa(sliceCPUWeight), "IOWeight="+strconv.Itoa(sliceIOWeight)); err != nil {
		return fmt.Errorf("crate's slice %s: its line and weights: %w", unit, err)
	}
	out, err := runTool("systemctl --user show", limit.Systemd(), managerEnv(),
		"systemctl", "--user", "show", "--property=ControlGroup", "--value", unit)
	if err != nil {
		return fmt.Errorf("crate's slice %s: %w", unit, err)
	}
	group := strings.TrimSpace(out)
	data, err := os.ReadFile(filepath.Join("/sys/fs/cgroup", group, "cgroup.controllers"))
	if err != nil {
		return fmt.Errorf("crate's slice %s: its controllers: %w", unit, err)
	}
	if missing := lacking(strings.Fields(string(data))); len(missing) > 0 {
		return fmt.Errorf("crate's slice %s lacks the %s controller, so crate cannot bound its work: root delegates it "+
			"to the user's manager (Delegate=cpu io memory pids in a drop-in for user@.service), effective when the manager restarts",
			unit, strings.Join(missing, " and "))
	}
	return nil
}

// lacking is each of controllers that have does not hold.
func lacking(have []string) []string {
	var missing []string
	for _, c := range controllers {
		if !slices.Contains(have, c) {
			missing = append(missing, c)
		}
	}
	return missing
}

// memTotal is the machine's memory in bytes, from /proc/meminfo.
func memTotal() (uint64, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, fmt.Errorf("the machine's memory: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, "MemTotal:"); ok {
			fields := strings.Fields(rest)
			if len(fields) == 2 && fields[1] == "kB" {
				if kb, err := strconv.ParseUint(fields[0], 10, 64); err == nil && kb > 0 {
					return kb * 1024, nil
				}
			}
		}
	}
	return 0, fmt.Errorf("the machine's memory: /proc/meminfo has no MemTotal in kB")
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
		prog, args, _ := Placement(leaf).Command("true")
		return systemd("systemd-run --user", append([]string{prog}, args...)...)
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
