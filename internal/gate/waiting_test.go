package gate

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWaitingIsTheDepthAndTheOldestChangesAge(t *testing.T) {
	r := &Repo{Bare: t.TempDir()}
	if d, o, err := r.Waiting(time.Now()); err != nil || d != 0 || o != 0 {
		t.Fatalf("an empty queue: depth %d, oldest %v, err %v", d, o, err)
	}
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	os.MkdirAll(filepath.Join(r.Bare, "crate"), 0o755)
	q := `[{"sha":"a","at":"2026-11-20T11:30:00Z"},{"sha":"b","at":"2026-11-20T11:55:00Z"}]`
	if err := os.WriteFile(filepath.Join(r.Bare, "crate", "queue.json"), []byte(q), 0o644); err != nil {
		t.Fatal(err)
	}
	d, o, err := r.Waiting(now)
	if err != nil || d != 2 || o != 30*time.Minute {
		t.Fatalf("depth %d, oldest %v, err %v; want 2, 30m", d, o, err)
	}
}
