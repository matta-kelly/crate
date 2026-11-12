package machine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/matta-kelly/crate/internal/limit"
)

// ceiling is a machine file's ceiling, placed in the user's systemd under the slice CRATE_SLICE names.
type ceiling struct {
	cfg   *Config
	path  string
	slice string
}

func (m *ceiling) Place(kind, system string) (Placement, error) {
	if err := CheckKind(kind); err != nil {
		return "", err
	}
	if err := checkCPUs(m.path, m.cfg.CPUs); err != nil {
		return "", err
	}
	base := m.slice + ".slice"
	leaf := m.leaf(kind, system)
	if err := startSlice(leaf); err != nil {
		return "", managerFailure(err)
	}
	if err := systemd("systemctl --user set-property", "systemctl", "--user", "set-property", "--runtime", base,
		"AllowedCPUs="+m.cfg.CPUs, "MemoryMax="+memoryBytes(m.cfg.Memory), "MemorySwapMax=0"); err != nil {
		return "", basePropertyFailure(base, m.cfg.CPUs, err)
	}
	kindSlice := m.slice + "-" + kind + ".slice"
	if err := systemd("systemctl --user set-property", "systemctl", "--user", "set-property", "--runtime",
		kindSlice, "CPUWeight="+strconv.Itoa(cpuWeight(kind))); err != nil {
		return "", fmt.Errorf("the %s kind's slice %s: CPUWeight: %w", kind, kindSlice, err)
	}
	return Placement(leaf), nil
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

func memoryBytes(memory string) string {
	n, err := strconv.ParseInt(memory[:len(memory)-1], 10, 64)
	if err != nil {
		return memory
	}
	switch memory[len(memory)-1] {
	case 'K':
		n *= 1024
	case 'M':
		n *= 1024 * 1024
	case 'G':
		n *= 1024 * 1024 * 1024
	}
	return strconv.FormatInt(n, 10)
}

// checkCPUs refuses a CPU the machine cannot have, so a slice never gets an empty cpuset from a bad file.
func checkCPUs(machineFile, spec string) error {
	data, err := os.ReadFile(possibleCPUs)
	if err != nil {
		return fmt.Errorf("machine file %s: cpus = %q: %s: %w", machineFile, spec, possibleCPUs, err)
	}
	possibleText := strings.TrimSpace(string(data))
	possible, err := cpuRanges(possibleText)
	if err != nil {
		return fmt.Errorf("machine file %s: cpus = %q: %s = %q: not a CPU list", machineFile, spec, possibleCPUs, possibleText)
	}
	wanted, err := cpuRanges(spec)
	if err != nil {
		return fmt.Errorf("machine file %s: cpus = %q: not a CPU list", machineFile, spec)
	}
	for _, span := range wanted {
		for cpu := span[0]; cpu <= span[1]; cpu++ {
			if !inRanges(possible, cpu) {
				return fmt.Errorf("machine file %s: cpus = %q: CPU %d is not one the machine has (%s)",
					machineFile, spec, cpu, possibleText)
			}
		}
	}
	return nil
}

const possibleCPUs = "/sys/devices/system/cpu/possible"

func cpuRanges(spec string) ([][2]int, error) {
	var ranges [][2]int
	for _, part := range strings.Split(spec, ",") {
		loText, hiText, hasRange := strings.Cut(part, "-")
		lo, err := strconv.Atoi(loText)
		if err != nil {
			return nil, err
		}
		hi := lo
		if hasRange {
			if hi, err = strconv.Atoi(hiText); err != nil {
				return nil, err
			}
		}
		ranges = append(ranges, [2]int{lo, hi})
	}
	return ranges, nil
}

func inRanges(ranges [][2]int, cpu int) bool {
	for _, span := range ranges {
		if cpu >= span[0] && cpu <= span[1] {
			return true
		}
	}
	return false
}

// basePropertyFailure adds the way to delegate cpuset when that is why the manager's set-property failed.
func basePropertyFailure(base, cpus string, err error) error {
	advice := cpusetAdvice()
	if advice != "" {
		return fmt.Errorf("the machine's slice %s: AllowedCPUs=%s: %s: %w", base, cpus, advice, err)
	}
	return fmt.Errorf("the machine's slice %s: AllowedCPUs=%s: %w", base, cpus, err)
}

// cpusetAdvice says how to delegate cpuset when the user's manager does not have it.
func cpusetAdvice() string {
	controllers, ok := managerControllers()
	if !ok || strings.Contains(controllers, "cpuset") {
		return ""
	}
	return fmt.Sprintf("cpuset is not delegated to the user's manager (%s): delegate it with Delegate=cpuset", controllers)
}

// managerControllers reads the user manager's cgroup.controllers. A manager that does not expose the cgroup
// file is asked for its DelegateControllers property instead, with the unit after -- (systemd 262 reads a bare
// -.slice as an option).
func managerControllers() (string, bool) {
	uid := strconv.Itoa(os.Getuid())
	for _, path := range []string{
		"/sys/fs/cgroup/user.slice/user-" + uid + ".slice/user@" + uid + ".service/cgroup.controllers",
		"/sys/fs/cgroup/user.slice/user-" + uid + ".slice/cgroup.controllers",
	} {
		data, err := os.ReadFile(path)
		if err == nil {
			return strings.TrimSpace(string(data)), true
		}
	}
	out, err := systemdOutput("systemctl --user show", "systemctl", "--user", "show", "--value",
		"-p", "DelegateControllers", "--", "-.slice")
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(out), true
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

// managerFailure says how to bring the user's manager up when the call could not reach it.
func managerFailure(err error) error {
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "failed to connect to bus") || strings.Contains(msg, "no medium found") ||
		strings.Contains(msg, "connection refused") || strings.Contains(msg, "no such file or directory") {
		return fmt.Errorf("%w: the user's manager is not running; the user needs linger (loginctl enable-linger)", err)
	}
	return err
}

// systemd runs one systemd call with its limit. Its error holds the call's name and systemd's own message.
func systemd(name string, args ...string) error {
	_, err := systemdOutput(name, args...)
	return err
}

func systemdOutput(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), limit.Systemd())
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Env = managerEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("%s did not finish within %v", name, limit.Systemd())
	}
	if err != nil {
		msg := strings.TrimSpace(out.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s: %s", name, msg)
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
