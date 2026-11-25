package status

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepo(t *testing.T) {
	for remote, want := range map[string]string{
		"git@github.com:owner/name.git":     "owner/name",
		"ssh://git@github.com/owner/name":   "owner/name",
		"https://github.com/owner/name.git": "owner/name",
		"/tmp/x/github/a.git":               "github/a",
	} {
		if got, err := Repo(remote); err != nil || got != want {
			t.Errorf("Repo(%q) = %q, %v; want %q", remote, got, err, want)
		}
	}
	if _, err := Repo("name"); err == nil {
		t.Error("a remote with no owner is refused")
	}
}

// token puts a token file in a config folder of the test's own.
func token(t *testing.T, value string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if value == "" {
		return
	}
	if err := os.MkdirAll(filepath.Join(dir, "crate"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "crate", "github-token"), []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPost(t *testing.T) {
	token(t, "tok")
	var path, auth string
	var body map[string]string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer api.Close()
	t.Setenv(apiEnv, api.URL)

	if err := Post("owner/name", "abc", Failure, strings.Repeat("x", 200)); err != nil {
		t.Fatal(err)
	}
	if path != "/repos/owner/name/statuses/abc" || auth != "Bearer tok" {
		t.Errorf("path %q, auth %q", path, auth)
	}
	if body["state"] != Failure || body["context"] != Context || len([]rune(body["description"])) != 140 {
		t.Errorf("body %v", body)
	}
}

func TestPostRefused(t *testing.T) {
	token(t, "tok")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"Resource not accessible"}`, http.StatusForbidden)
	}))
	defer api.Close()
	t.Setenv(apiEnv, api.URL)
	if err := Post("owner/name", "abc", Success, ""); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("a refused post is its error: %v", err)
	}
}

func TestPostNoToken(t *testing.T) {
	token(t, "")
	if err := Post("owner/name", "abc", Success, ""); !errors.Is(err, ErrNoToken) {
		t.Errorf("no token file: %v", err)
	}
}
