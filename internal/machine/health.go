package machine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/matta-kelly/crate/internal/limit"
	"github.com/matta-kelly/crate/internal/process"
)

// The machine's health: crate's own failures in a row (errors, never a change's fail), and the pause they make. After
// PauseAfter errors in a row no check starts, so a sick machine never turns its own failure into a verdict on a change.
// A pass sets the count back and ends a pause; so does a probe that passes (crate apply). It is one file for every repo
// of the machine's user: $XDG_STATE_HOME/crate/health.json.

// PauseAfter is how many errors in a row pause the machine.
const PauseAfter = 3

// Health is the machine's run of errors and its pause: Paused is the last error's text, "" while it runs.
type Health struct {
	Errors int       `json:"errors"`
	Paused string    `json:"paused,omitempty"`
	Since  time.Time `json:"since,omitzero"` // when it paused
}

func healthPath() string { return filepath.Join(stateHome(), "crate", "health.json") }

// ReadHealth is the machine's health; none is a machine that runs.
func ReadHealth() (Health, error) {
	var h Health
	data, err := os.ReadFile(healthPath())
	if errors.Is(err, os.ErrNotExist) {
		return h, nil
	}
	if err != nil {
		return h, err
	}
	if err := json.Unmarshal(data, &h); err != nil {
		return h, fmt.Errorf("%s: %w", healthPath(), err)
	}
	return h, nil
}

// Erred counts one error, why, and pauses the machine at the PauseAfter-th in a row. It gives the health after.
func Erred(why string) (Health, error) {
	return changeHealth(func(h *Health) {
		h.Errors++
		if h.Errors >= PauseAfter && h.Paused == "" {
			h.Paused, h.Since = why, time.Now().UTC()
		}
	})
}

// Passed says the machine ran a check through: the count goes back to 0 and a pause ends.
func Passed() error {
	_, err := changeHealth(func(h *Health) { *h = Health{} })
	return err
}

// changeHealth changes the health under its lock and writes it whole, by a rename.
func changeHealth(change func(*Health)) (Health, error) {
	dir := filepath.Dir(healthPath())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Health{}, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "health.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return Health{}, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return Health{}, err
	}
	h, err := ReadHealth()
	if err != nil {
		return h, err
	}
	change(&h)
	data, err := json.Marshal(h)
	if err != nil {
		return h, err
	}
	tmp := healthPath() + ".new"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return h, err
	}
	return h, os.Rename(tmp, healthPath())
}

// Probe asks podman what a check needs of it, each within limit.Podman: its info, its image store, and a network
// made and removed. nil is a machine that answers.
func Probe(ctx context.Context) error {
	network := "crate-probe-" + strconv.Itoa(os.Getpid())
	calls := [][]string{
		{"info", "--format", "{{.Host.Arch}}"},
		{"images", "--quiet"},
		{"network", "create", network},
		{"network", "rm", "--force", network},
	}
	for _, args := range calls {
		if _, err := process.Output(ctx, process.Call{Prog: "podman", Args: args, Limit: limit.Podman()}); err != nil {
			if args[0] == "network" {
				_, _ = process.Output(context.Background(), process.Call{Prog: "podman", Args: []string{"network", "rm", "--force", network},
					Limit: limit.Podman()})
			}
			return err
		}
	}
	return nil
}
