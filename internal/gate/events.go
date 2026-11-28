package gate

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Events: crate says on this server what it did, for whoever watches, so the step that follows need not wait for its
// timer. Each event is a file under $XDG_RUNTIME_DIR/crate/events (/run/user/<uid> when unset) that crate writes a
// line to, "<repo> <commit> <time>", each time it happens; a systemd path unit that watches the file starts what
// follows. Nothing waits on an event, and none is needed: each watcher's timer still runs.
//   - published: a green main's image, or a declared image, went to its registry, so an update bot can propose it
//   - mirrored-<repo>: the repo's main went to its GitHub copy, so a node that pulls it can sync
const (
	EventPublished = "published"
	eventMirrored  = "mirrored-"
)

// EventsDir is the folder of crate's events.
func EventsDir() string {
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if runtime == "" {
		runtime = "/run/user/" + strconv.Itoa(os.Getuid())
	}
	return filepath.Join(runtime, "crate", "events")
}

// event writes the event name for commit. A write that fails is no error of the step that made it: the watcher's
// timer covers it.
func (r *Repo) event(name, commit string) {
	dir := EventsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	line := fmt.Sprintf("%s %s %s\n", r.Name(), commit, time.Now().UTC().Format(time.RFC3339))
	_ = os.WriteFile(filepath.Join(dir, name), []byte(line), 0o644)
}
