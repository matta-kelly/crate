package gate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An event is a line in its file under XDG_RUNTIME_DIR/crate/events, written again each time it happens.
func TestAnEventIsALineInItsFile(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	f := newFixture(t, "batch = 1\n")
	f.repo.event(EventPublished, "abc")
	f.repo.event(EventPublished, "def")
	data, err := os.ReadFile(filepath.Join(EventsDir(), EventPublished))
	if err != nil {
		t.Fatal(err)
	}
	if fields := strings.Fields(string(data)); len(fields) != 3 || fields[0] != f.repo.Name() || fields[1] != "def" {
		t.Errorf("the event's line: %q", data)
	}
}

// A copy of main to GitHub is the event mirrored-<repo>.
func TestACopyOfMainIsTheEventMirrored(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	f := newFixture(t, "batch = 1\n")
	github := filepath.Join(t.TempDir(), "github.git")
	f.git(f.client, "init", "-q", "--bare", "-b", "main", github)
	f.git(f.repo.Bare, "remote", "add", "github", github)
	main := f.git(f.repo.Bare, "rev-parse", "main")
	if err := f.repo.CopyMain(main); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(EventsDir(), "mirrored-"+f.repo.Name()))
	if err != nil || !strings.Contains(string(data), main) {
		t.Errorf("mirrored: %q, %v", data, err)
	}
}
