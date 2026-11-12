package machine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/matta-kelly/crate/internal/limit"
)

// Hold takes a slot of the kind for the system. A slot belongs to the system: a second Hold of the same kind
// and system in it shares the first. Work has no count, so a person's work never waits for room.
func (m *ceiling) Hold(kind, system string) (Slot, error) {
	if err := CheckKind(kind); err != nil {
		return nil, err
	}
	count := -1
	switch kind {
	case Check:
		count = m.cfg.Count.Check
	case Postsubmit:
		count = m.cfg.Count.Postsubmit
	case Attempt:
		count = m.cfg.Count.Attempt
	}
	root := filepath.Join(stateHome(), "crate", "slots")
	dir := filepath.Join(root, kind)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(root, "."+kind+".lock")
	deadline := time.Now().Add(limit.Hold())
	for {
		path, took, holders, err := m.takeSlot(dir, lockPath, kind, system, count)
		if err != nil {
			return nil, err
		}
		if took {
			return &heldSlot{path: path}, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("machine: hold %s: count %d, limit %s: held by %s",
				kind, count, limit.Hold(), strings.Join(holders, ", "))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// takeSlot looks for a free slot and writes one when there is room, all under the kind's short lock.
func (m *ceiling) takeSlot(dir, lockPath, kind, system string, count int) (string, bool, []string, error) {
	unlock, err := lockFolder(lockPath, limit.SlotLock())
	if err != nil {
		return "", false, nil, err
	}
	defer unlock()

	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false, nil, err
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		files = append(files, filepath.Join(dir, entry.Name()))
	}

	// The same system's own slot is shared, never taken twice.
	for _, file := range files {
		rec, ok := readRecord(file)
		if !ok {
			_ = os.Remove(file)
			continue
		}
		if rec.System == system && !m.free(rec) {
			return file, true, nil, nil
		}
	}

	var kept, holders []string
	for _, file := range files {
		rec, ok := readRecord(file)
		if !ok {
			_ = os.Remove(file)
			continue
		}
		if m.free(rec) {
			_ = os.Remove(file)
			continue
		}
		kept = append(kept, file)
		holders = append(holders, rec.System)
	}
	if count >= 0 && len(kept) >= count {
		return "", false, holders, nil
	}
	file, err := writeSlot(dir, system)
	return file, err == nil, holders, err
}

type record struct {
	System string
	PID    int
	Start  string
	Bound  bool
}

func readRecord(path string) (record, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return record{}, false
	}
	var rec record
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "system":
			rec.System = value
		case "pid":
			rec.PID, _ = strconv.Atoi(value)
		case "start":
			rec.Start = value
		case "bound":
			rec.Bound = value == "true"
		}
	}
	return rec, rec.System != "" && rec.PID != 0 && rec.Start != ""
}

func writeRecord(path string, rec record) error {
	body := fmt.Sprintf("system=%s\npid=%d\nstart=%s\nbound=%t\n", rec.System, rec.PID, rec.Start, rec.Bound)
	return os.WriteFile(path, []byte(body), 0o600)
}

func writeSlot(dir, system string) (string, error) {
	start, err := processStart(os.Getpid())
	if err != nil {
		return "", err
	}
	file, err := os.CreateTemp(dir, ".tmp-")
	if err != nil {
		return "", err
	}
	temp := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(temp)
		return "", err
	}
	rec := record{System: system, PID: os.Getpid(), Start: start}
	if err := writeRecord(temp, rec); err != nil {
		_ = os.Remove(temp)
		return "", err
	}
	path := filepath.Join(dir, "slot-"+strings.TrimPrefix(filepath.Base(temp), ".tmp-"))
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return "", err
	}
	return path, nil
}

// free is the whole life of a slot: no holder left before its network, or its network gone after the bind.
func (m *ceiling) free(rec record) bool {
	if !rec.Bound {
		return !processAlive(rec.PID, rec.Start)
	}
	exists, err := networkExists(rec.System)
	if err != nil {
		fmt.Fprintf(os.Stderr, "crate: %v: the slot of %s stays held\n", err, rec.System)
		return false
	}
	return !exists
}

func processAlive(pid int, start string) bool {
	got, err := processStart(pid)
	return err == nil && got == start
}

// processStart is field 22 of /proc/<pid>/stat: the moment the process started, so a reused pid is not it.
func processStart(pid int) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	close := strings.LastIndex(string(data), ")")
	if close < 0 {
		return "", fmt.Errorf("the stat of pid %d has no comm", pid)
	}
	fields := strings.Fields(string(data)[close+1:])
	if len(fields) < 20 {
		return "", fmt.Errorf("the stat of pid %d has %d fields after comm", pid, len(fields))
	}
	return fields[19], nil
}

func networkExists(system string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), limit.Podman())
	defer cancel()
	cmd := exec.CommandContext(ctx, "podman", "network", "exists", system)
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return false, fmt.Errorf("podman network exists %s did not finish within %v", system, limit.Podman())
	}
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	msg := strings.TrimSpace(out.String())
	if msg == "" {
		msg = err.Error()
	}
	return false, fmt.Errorf("podman network exists %s: %s", system, msg)
}

func lockFolder(path string, wait time.Duration) (func(), error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
				_ = file.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = file.Close()
			return nil, fmt.Errorf("the lock %s: %w", path, err)
		}
		if time.Now().After(deadline) {
			_ = file.Close()
			return nil, fmt.Errorf("the lock %s: waited %v", path, wait)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func stateHome() string {
	if state := os.Getenv("XDG_STATE_HOME"); state != "" {
		return state
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, ".local", "state")
}

type heldSlot struct{ path string }

func (s *heldSlot) Bind() {
	rec, ok := readRecord(s.path)
	if !ok {
		return
	}
	rec.Bound = true
	replaceRecord(s.path, rec)
}

// replaceRecord writes the new record beside the slot and moves it over the old one, so a Hold that looks at the
// same moment never reads half a record and takes the slot for free.
func replaceRecord(path string, rec record) {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".tmp-")
	if err != nil {
		return
	}
	temp := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(temp)
		return
	}
	if err := writeRecord(temp, rec); err != nil {
		_ = os.Remove(temp)
		return
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
	}
}

func (s *heldSlot) Release() { _ = os.Remove(s.path) }
