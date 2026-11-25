// Package status writes crate's verdict on a change from the intake to the repo's GitHub copy: a commit status named
// crate on the change's head, which the update bot and its pull request show. A status tells the verdict and never
// makes it: a post that fails is the caller's to record, and the change is decided as if it had not been tried.
package status

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"

	"github.com/matta-kelly/crate/internal/limit"
)

// Context is the status's name on GitHub: one per commit, so a later post replaces an earlier one.
const Context = "crate"

// A state, as GitHub names them: pending while the change waits or is checked, then the verdict.
const (
	Pending = "pending"
	Success = "success" // landed
	Failure = "failure" // refused: the change's own fault
	Error   = "error"   // crate could not check it
)

// ErrNoToken: the token file is not there, so no status is posted.
var ErrNoToken = errors.New("no GitHub token")

// apiEnv names GitHub's REST API to post to, for a test that stands in for it; unset, it is GitHub's own.
const apiEnv = "CRATE_GITHUB_API"

// TokenFile is the token the posts send: $XDG_CONFIG_HOME/crate/github-token, ~/.config when it is unset. A
// fine-grained token with commit statuses write, on the repos crate serves, and nothing else. Only a status post
// reads it; no check, postsubmit or workspace mounts it.
func TokenFile() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = home + "/.config"
	}
	return dir + "/crate/github-token"
}

// Repo is owner/name from a github remote's url: git@github.com:owner/name.git, ssh:// or https://, or a path, whose
// last two parts it takes.
func Repo(remote string) (string, error) {
	s := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(remote), "/"), ".git")
	if rest, ok := strings.CutPrefix(s, "git@"); ok {
		_, s, _ = strings.Cut(rest, ":")
	}
	parts := strings.Split(s, "/")
	if len(parts) < 2 || parts[len(parts)-2] == "" || parts[len(parts)-1] == "" {
		return "", fmt.Errorf("the remote %q names no owner/name", remote)
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1], nil
}

// Post writes state on sha in repo (owner/name), with description cut to GitHub's 140 characters.
func Post(repo, sha, state, description string) error {
	raw, err := os.ReadFile(TokenFile())
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w at %s", ErrNoToken, TokenFile())
	}
	if err != nil {
		return err
	}
	token := strings.TrimSpace(string(raw))
	if r := []rune(description); len(r) > 140 {
		description = string(r[:139]) + "…"
	}
	body, err := json.Marshal(map[string]string{"state": state, "context": Context, "description": description})
	if err != nil {
		return err
	}
	api := os.Getenv(apiEnv)
	if api == "" {
		api = "https://api.github.com"
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit.GitHub())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, api+"/repos/"+repo+"/statuses/"+sha, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("the status post to %s: %w", repo, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("the status post to %s: %s: %s", repo, resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}
