package gate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/matta-kelly/crate/internal/status"
)

// A change from the intake gets crate's commit status at its verdict: success when it lands, failure when its check
// refuses it. A change a person pushed gets none.
func TestAnIntakeChangeGetsItsVerdictAsAStatus(t *testing.T) {
	f := newFixture(t, "batch = 1\n")
	github := filepath.Join(t.TempDir(), "owner", "name.git")
	f.git(f.client, "init", "-q", "--bare", "-b", "main", github)
	f.git(f.repo.Bare, "remote", "add", "github", github)

	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	if err := os.MkdirAll(filepath.Join(config, "crate"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "crate", "github-token"), []byte("tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	posts := map[string][]string{} // each commit's states, in order
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		sha, ok := strings.CutPrefix(r.URL.Path, "/repos/owner/name/statuses/")
		if !ok || body["context"] != status.Context {
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
			return
		}
		mu.Lock()
		posts[sha] = append(posts[sha], body["state"])
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	defer api.Close()
	t.Setenv("CRATE_GITHUB_API", api.URL)

	a := f.changeFrom("a", map[string]string{"a": "a\n"}, "renovate/a")
	b := f.changeFrom("b", map[string]string{"red": "x\n"}, "renovate/b")
	c := f.change("c", map[string]string{"c": "c\n"})
	f.work(2)

	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(posts[a], ","); got != status.Success {
		t.Errorf("a landed: expected success, got %q", got)
	}
	if got := strings.Join(posts[b], ","); got != status.Failure {
		t.Errorf("b refused: expected failure, got %q", got)
	}
	if len(posts[c]) != 0 {
		t.Errorf("c is a person's: expected no status, got %v", posts[c])
	}
}
